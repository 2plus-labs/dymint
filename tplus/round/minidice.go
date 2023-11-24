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
	//go func() {
	//	time.Sleep(1 * time.Second)
	//	err := m.maybeRecover()
	//	if err != nil {
	//		m.logger.Error("minidice round maybeRecover", "err", err)
	//		panic(err)
	//	}
	//}()
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
			//m.logger.Info("active game:", "denom", ag.Denom, "state", ag.State, "game_id", ag.GameId)
			switch ag.State {
			case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED, minidicetypes.RoundState_ROUND_STATE_FINALIZED:
				msgRound = append(msgRound, queue.MsgInQueue{
					Messages: []sdk.Msg{minidicetypes.NewMsgStartRound(m.creatorAddr, ag.GameId)},
					TimeExec: time.Now().Unix(),
				})
				//err := m.startRound(ag.GameId)
				//if err != nil {
				//	m.logger.Error("recover call startRound err", "error", err)
				//	panic(err)
				//}
			case minidicetypes.RoundState_ROUND_STATE_STARTED:
				timeNow := time.Now().UTC().Unix()
				timeEndRound := ag.EndRound
				diffSec := timeNow - timeEndRound
				m.logger.Info("maybeRecover", "diff time", diffSec)
				//var err error
				if diffSec > 0 {
					msgRound = append(msgRound, queue.MsgInQueue{
						Messages: []sdk.Msg{minidicetypes.NewMsgEndRound(m.creatorAddr, ag.GameId)},
						TimeExec: time.Now().Unix(),
					})
					//err = m.endRound(ag.GameId)
				} else {
					msgRound = append(msgRound, queue.MsgInQueue{
						Messages: []sdk.Msg{minidicetypes.NewMsgEndRound(m.creatorAddr, ag.GameId)},
						TimeExec: time.Now().Unix() - diffSec,
					})
					//t := time.NewTicker(time.Duration(-diffSec) * time.Second)
					//defer t.Stop()
					//<-t.C
					//err = m.endRound(ag.GameId)
				}

				//if err != nil {
				//	m.logger.Error("recover call endRound err", "error", err)
				//	panic(err)
				//}
			case minidicetypes.RoundState_ROUND_STATE_ENDED:
				msgRound = append(msgRound, queue.MsgInQueue{
					Messages: []sdk.Msg{&minidicetypes.MsgFinalizeRound{Creator: m.creatorAddr, GameId: ag.GameId}},
					TimeExec: time.Now().Unix(),
				})
				//err := m.finalizeRound(ag.GameId)
				//if err != nil {
				//	m.logger.Error("recover call startRound err", "error", err)
				//	panic(err)
				//}
			}
			//time.Sleep(200 * time.Millisecond)
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
		m.logger.Error("FilterRoundEvent", "deliverTx", deliverTx.String())
		if deliverTx.Code != 0 {
			continue
		}

		// filter init game event
		events := FindEventsByType(deliverTx.Events, minidicetypes.EventTypeInitGame)
		spew.Dump("init_game", events)
		if len(events) > 0 {
			msg, err := m.FilterInitGameEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			m.logger.Error("FilterRoundEvent", "msg_init", spew.Sdump(msg))
			//m.queueExec.PushBatch(msg)
			msgs = append(msgs, msg...)
		}

		// filter start round event
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeStartRound)
		spew.Dump("start_round", events)
		if len(events) > 0 {
			msg, err := m.FilterStartRoundEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			m.logger.Error("FilterRoundEvent", "msg_start", spew.Sdump(msg))
			//m.queueExec.PushBatch(msg)
			msgs = append(msgs, msg...)
		}

		// filter end round event
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeEndRound)
		spew.Dump("end_round", events)
		if len(events) > 0 {
			msg, err := m.FilterEndRoundEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			m.logger.Error("FilterRoundEvent", "msg_end", spew.Sdump(msg))
			//m.queueExec.PushBatch(msg)
			msgs = append(msgs, msg...)
		}

		// filter finalize round event
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeFinalizeRound)
		spew.Dump("finalize_round", events)
		if len(events) > 0 {
			msg, err := m.FilterFinalizeRoundEvent(events, m.creatorAddr)
			if err != nil {
				m.logger.Error("FilterRoundEvent", "error", err)
				return nil, err
			}
			m.logger.Error("FilterRoundEvent", "msg_finalize", spew.Sdump(msg))
			//m.queueExec.PushBatch(msg)
			msgs = append(msgs, msg...)
		}
	}
	//if len(msgs) > 0 {
	m.logger.Error("FilterRoundEvent", "msg", spew.Sdump(msgs))
	//	//m.eventChannel <- msgs
	//}

	return msgs, nil
}

func (m *MinidiceRound) FilterInitGameEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
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

func (m *MinidiceRound) FilterStartRoundEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
	var inQueues []queue.MsgInQueue
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return nil, fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		m.sinceStartsMu.Lock()
		defer m.sinceStartsMu.Unlock()
		m.sinceStarts[gameId] = time.Now().Unix()

		inQueues = append(inQueues, queue.MsgInQueue{
			Messages: []sdk.Msg{minidicetypes.NewMsgEndRound(creator, gameId)},
			TimeExec: time.Now().Unix() + int64(m.options.StartRoundInterval),
		})
	}

	return inQueues, nil
}

func (m *MinidiceRound) FilterEndRoundEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
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

func (m *MinidiceRound) FilterFinalizeRoundEvent(events []abci.Event, creator string) ([]queue.MsgInQueue, error) {
	var msgInQueues []queue.MsgInQueue
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return nil, fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		msg := minidicetypes.NewMsgStartRound(creator, gameId)
		m.logger.Info("FilterFinalizeRoundEvent", "msg", spew.Sdump(msg))
		msgInQueue := queue.MsgInQueue{
			Messages: []sdk.Msg{msg},
		}
		m.sinceStartsMu.Lock()
		defer m.sinceStartsMu.Unlock()
		t := m.sinceStarts[gameId]

		startedIn := time.Now().Unix() - t
		diff := m.options.RoundInterval - int(startedIn)
		if diff > 0 {
			msgInQueue.TimeExec = time.Now().Unix() + int64(diff)
		}
		msgInQueue.TimeExec = time.Now().Unix()

		msgInQueues = append(msgInQueues, msgInQueue)
	}

	return msgInQueues, nil
}

func (m *MinidiceRound) PubEvents(state *tmstate.ABCIResponses) {
	m.eventChannel <- state
	m.logger.Info("PubEvents", "state", spew.Sdump(state))
}

func (m *MinidiceRound) handleMsgQueueLoops() {
	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info("handleMsgQueueLoops", "ctx done")
			return
		case states := <-m.eventChannel:
			//m.logger.Info("handleMsgQueueLoops", "states", spew.Sdump(states))
			msgs, err := m.FilterRoundEvent(states)
			if err != nil {
				m.logger.Error("handleMsgQueueLoops", "error", err)
				panic(err)
			}
			m.logger.Info("handleMsgQueueLoops", "msgs", spew.Sdump(msgs))
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
