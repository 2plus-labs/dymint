package round

import (
	"context"
	"fmt"
	"sync"
	"time"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/avast/retry-go"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/davecgh/go-spew/spew"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
	"github.com/dymensionxyz/dymint/tplus/executor"
	"github.com/dymensionxyz/dymint/tplus/queue"
	abci "github.com/tendermint/tendermint/abci/types"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
)

type MinidiceOptions struct {
	// All interval is by seconds
	StartRoundInterval int
	RoundInterval      int
}

func DefaultOptions() *MinidiceOptions {
	return &MinidiceOptions{
		StartRoundInterval: 45,
		RoundInterval:      60,
	}
}

type MinidiceRound struct {
	ctx        context.Context
	cancelFunc context.CancelFunc
	logger     log.Logger
	options    *MinidiceOptions
	// tplusClient for query and broadcast tx to tplus chain app
	tplusClient *tplus.TplusClient
	tplusConfig *tplus.Config
	creator     string
	creatorAddr string

	// use to retry call startRound, endRound, finalizeRound
	ctlRoundRetryAttempts uint
	ctlRoundRetryDelay    time.Duration
	ctlRoundRetryMaxDelay time.Duration

	// temp cache round_id will remove later
	currentRound uint32

	// Time diff from started round in seconds by denom
	sinceStartsMu sync.Mutex
	sinceStarts   map[string]int64
	execBroadcast *executor.Executor
	eventChannel  chan *tmstate.ABCIResponses
}

func NewMinidiceRound(
	tplusConfig *tplus.Config,
	options *MinidiceOptions,
	logger log.Logger,
) (*MinidiceRound, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &MinidiceRound{
		ctx:                   ctx,
		cancelFunc:            cancel,
		options:               options,
		logger:                logger,
		creator:               tplusConfig.AccountName,
		currentRound:          0,
		ctlRoundRetryAttempts: 5,
		ctlRoundRetryDelay:    300 * time.Millisecond,
		ctlRoundRetryMaxDelay: 5 * time.Second,
		sinceStarts:           map[string]int64{},
		sinceStartsMu:         sync.Mutex{},
		eventChannel:          make(chan *tmstate.ABCIResponses, 500),
		tplusConfig:           tplusConfig,
	}

	return m, nil
}

func (m *MinidiceRound) Start() error {
	var tplusClient *tplus.TplusClient
	var err error
	retry.Do(func() error {
		tplusClient, err = tplus.NewTplusClient(m.tplusConfig)
		return err
	})

	if err != nil {
		m.logger.Error("init tplus client failed: %s", err)
		return err
	}
	m.tplusClient = tplusClient
	m.execBroadcast = executor.NewExecutor(m.ctx, m.logger, tplusClient, m.creator, 5, queue.NewQueue())

	creatorAddr, err := m.tplusClient.GetAccountAddress(m.creator)
	if err != nil {
		m.logger.Error("NewMinidiceRound", "creator", m.creator)
		return err
	}
	m.logger.Info("minidice round", "creator", m.creator, "creator addr", creatorAddr)
	m.creatorAddr = creatorAddr

	// start recover
	err = m.maybeRecover()
	if err != nil {
		m.logger.Error("minidice round maybeRecover", "err", err)
		return err
	}
	go func() {
		err := m.execBroadcast.Serve()
		if err != nil {
			m.logger.Error("minidice round execBroadcast", "err", err)
			panic(err)
		}
	}()
	go m.handleMsgQueueLoops()
	return nil
}

func (m *MinidiceRound) maybeRecover() error {
	// Recover state of all game active
	activeGames := m.getActiveGames()
	m.logger.Info("maybeRecover", "list active games:", activeGames)
	if len(activeGames) > 0 {
		msgRound := make([]queue.MsgInQueue, 0)
		for _, ag := range activeGames {
			switch ag.State {
			case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED, minidicetypes.RoundState_ROUND_STATE_FINALIZED:
				msgRound = append(msgRound, queue.MsgInQueue{
					Messages: []sdk.Msg{minidicetypes.NewMsgStartRound(m.creatorAddr, ag.GameId)},
					TimeExec: time.Now().Unix(),
				})
			case minidicetypes.RoundState_ROUND_STATE_STARTED:
				timeNow := time.Now().UTC().Unix()
				timeEndRound := ag.EndRound
				diffSec := timeNow - timeEndRound
				m.logger.Info("maybeRecover", "diff time", diffSec)
				if diffSec > 0 {
					msgRound = append(msgRound, queue.MsgInQueue{
						Messages: []sdk.Msg{minidicetypes.NewMsgEndRound(m.creatorAddr, ag.GameId)},
						TimeExec: time.Now().Unix(),
					})
				} else {
					msgRound = append(msgRound, queue.MsgInQueue{
						Messages: []sdk.Msg{minidicetypes.NewMsgEndRound(m.creatorAddr, ag.GameId)},
						TimeExec: time.Now().Unix() - diffSec,
					})
				}

			case minidicetypes.RoundState_ROUND_STATE_ENDED:
				msgRound = append(msgRound, queue.MsgInQueue{
					Messages: []sdk.Msg{&minidicetypes.MsgFinalizeRound{Creator: m.creatorAddr, GameId: ag.GameId}},
					TimeExec: time.Now().Unix(),
				})
			}
		}
		m.logger.Info("maybeRecover", "msgRound", spew.Sdump(msgRound))
		m.execBroadcast.PushBatch(msgRound)
	}
	return nil
}

func (m *MinidiceRound) Stop() error {
	m.logger.Info("Stopping minidice round")
	m.cancelFunc()
	err := m.tplusClient.StopEventListener()
	if err != nil {
		return err
	}
	return nil
}

func (m *MinidiceRound) getActiveGames() []*minidicetypes.ActiveGame {
	return m.tplusClient.GetActiveGames(m.ctx)
}

func (m *MinidiceRound) FilterRoundEvent(state *tmstate.ABCIResponses) ([]queue.MsgInQueue, error) {
	msgs := make([]queue.MsgInQueue, 0)
	for _, deliverTx := range state.DeliverTxs {
		m.logger.Debug("FilterRoundEvent", "deliverTx", deliverTx.String())
		if deliverTx.Code != 0 {
			continue
		}

		// filter init game event
		events := FindEventsByType(deliverTx.Events, minidicetypes.EventTypeInitGame)
		if len(events) > 0 {
			msg, err := m.filterInitGameEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			msgs = append(msgs, msg...)
		}

		// filter start round event
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeStartRound)
		if len(events) > 0 {
			msg, err := m.filterStartRoundEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			msgs = append(msgs, msg...)
		}

		// filter end round event
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeEndRound)
		if len(events) > 0 {
			msg, err := m.filterEndRoundEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			msgs = append(msgs, msg...)
		}

		// filter finalize round event
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeFinalizeRound)
		if len(events) > 0 {
			msg, err := m.filterFinalizeRoundEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			msgs = append(msgs, msg...)
		}
	}

	return msgs, nil
}

func (m *MinidiceRound) filterInitGameEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
	var msgStartRounds []queue.MsgInQueue
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return nil, fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		msgStartRounds = append(msgStartRounds, queue.MsgInQueue{
			Messages: []sdk.Msg{minidicetypes.NewMsgStartRound(creator, gameId)},
			TimeExec: time.Now().Unix(),
		})
	}

	return msgStartRounds, nil
}

func (m *MinidiceRound) filterStartRoundEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
	var inQueues []queue.MsgInQueue
	m.sinceStartsMu.Lock()
	defer m.sinceStartsMu.Unlock()
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return nil, fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		m.sinceStarts[gameId] = time.Now().Unix()

		inQueues = append(inQueues, queue.MsgInQueue{
			Messages: []sdk.Msg{minidicetypes.NewMsgEndRound(creator, gameId)},
			TimeExec: time.Now().Unix() + int64(m.options.StartRoundInterval),
		})
	}

	return inQueues, nil
}

func (m *MinidiceRound) filterEndRoundEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
	var msgInQueues []queue.MsgInQueue
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return nil, fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		msgInQueues = append(msgInQueues, queue.MsgInQueue{
			Messages: []sdk.Msg{&minidicetypes.MsgFinalizeRound{
				Creator: creator,
				GameId:  gameId,
			}},
			TimeExec: time.Now().Unix(),
		})
	}

	return msgInQueues, nil
}

func (m *MinidiceRound) filterFinalizeRoundEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
	var msgInQueues []queue.MsgInQueue
	m.sinceStartsMu.Lock()
	defer m.sinceStartsMu.Unlock()
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return nil, fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		msg := minidicetypes.NewMsgStartRound(creator, gameId)
		msgInQueue := queue.MsgInQueue{
			Messages: []sdk.Msg{msg},
		}

		t := m.sinceStarts[gameId]
		startedIn := time.Now().Unix() - t
		diff := m.options.RoundInterval - int(startedIn)
		if diff > 0 {
			msgInQueue.TimeExec = time.Now().Unix() + int64(diff)
		} else {
			msgInQueue.TimeExec = time.Now().Unix()

		}

		msgInQueues = append(msgInQueues, msgInQueue)
	}

	return msgInQueues, nil
}

func (m *MinidiceRound) PubEvents(state *tmstate.ABCIResponses) {
	m.eventChannel <- state
	m.logger.Debug("PubEvents", "state", spew.Sdump(state))
}

func (m *MinidiceRound) handleMsgQueueLoops() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case states := <-m.eventChannel:
			msgs, err := m.FilterRoundEvent(states)
			if err != nil {
				m.logger.Error("handleMsgQueueLoops", "error", err)
				panic(err)
			}
			m.logger.Debug("handleMsgQueueLoops", "msgs", spew.Sdump(msgs))
			if len(msgs) > 0 {
				m.execBroadcast.PushBatch(msgs)

			}
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (m *MinidiceRound) GetEventsChannel() chan *tmstate.ABCIResponses {
	return m.eventChannel
}
