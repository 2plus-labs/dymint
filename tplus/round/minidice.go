package round

import (
	"context"
	"fmt"
	"sync"
	"time"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/avast/retry-go"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
	"github.com/dymensionxyz/dymint/tplus/executor"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/pubsub"
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
	pubsub     *pubsub.Server
	options    *MinidiceOptions
	// tplusClient for query and broadcast tx to tplus chain app
	tplusClient *tplus.TplusClient
	creator     string
	creatorAddr string

	e *executor.Executor

	// Time diff from started round in seconds by denom
	sinceStartsMu sync.Mutex
	sinceStarts   map[string]int64
}

func NewMinidiceRound(
	tplusConfig *tplus.Config,
	options *MinidiceOptions,
	logger log.Logger,
	pubsub *pubsub.Server,
	creator string,
) (*MinidiceRound, error) {
	var tplusClient *tplus.TplusClient
	var err error
	retry.Do(func() error {
		tplusClient, err = tplus.NewTplusClient(tplusConfig)
		return err
	})

	if err != nil {
		logger.Error("init tplus client failed: %s", err)
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	m := &MinidiceRound{
		ctx:           ctx,
		cancelFunc:    cancel,
		options:       options,
		pubsub:        pubsub,
		logger:        logger,
		tplusClient:   tplusClient,
		creator:       creator,
		sinceStarts:   map[string]int64{},
		sinceStartsMu: sync.Mutex{},
	}

	creatorAddr, err := m.tplusClient.GetAccountAddress(m.creator)
	if err != nil {
		m.logger.Error("NewMinidiceRound", "creator", creator)
		return nil, err
	}
	logger.Info("minidice round", "creator", creator, "creator addr", creatorAddr)
	m.creatorAddr = creatorAddr

	e := executor.NewExecutor(ctx, logger, tplusClient, m.creator, 10)
	m.e = e

	return m, nil
}

func (m *MinidiceRound) Start() error {
	activeGames := m.getActiveGames()
	if len(activeGames) > 0 {
		go m.recoverActiveGame(activeGames)
	}
	return nil
}

func (m *MinidiceRound) delayPush(msg sdk.Msg, delay time.Duration) {
	t := time.NewTicker(delay)
	defer t.Stop()
	select {
	case <-m.ctx.Done():
		return
	case <-t.C:
		m.e.Push(msg)
	}
}

func (m *MinidiceRound) recoverActiveGame(activeGames []*minidicetypes.ActiveGame) {
	time.Sleep(200 * time.Millisecond)
	for _, ag := range activeGames {
		switch ag.State {
		case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED, minidicetypes.RoundState_ROUND_STATE_FINALIZED:
			msg := &minidicetypes.MsgStartRound{
				GameId:  ag.GameId,
				Creator: m.creatorAddr,
			}
			m.e.Push(msg)
		case minidicetypes.RoundState_ROUND_STATE_STARTED:
			timeNow := time.Now().UTC().Unix()
			timeEndRound := ag.EndRound
			diffSec := timeNow - timeEndRound
			msg := &minidicetypes.MsgEndRound{
				Creator: m.creatorAddr,
				GameId:  ag.GameId,
			}
			if diffSec > 0 {
				m.e.Push(msg)
			} else {
				delay := time.Duration(-diffSec) * time.Second
				go m.delayPush(msg, delay)
			}
		case minidicetypes.RoundState_ROUND_STATE_ENDED:
			msg := &minidicetypes.MsgFinalizeRound{
				GameId:  ag.GameId,
				Creator: m.creatorAddr,
			}
			m.e.Push(msg)
		}
	}
}

func (m *MinidiceRound) Stop() error {
	m.logger.Info("Stopping minidice round")
	m.cancelFunc()
	return nil
}

func (m *MinidiceRound) getActiveGames() []*minidicetypes.ActiveGame {
	return m.tplusClient.GetActiveGames(m.ctx)
}

func (m *MinidiceRound) startRoundOrDelay(gameId string, delay time.Duration) {
	msg := &minidicetypes.MsgStartRound{
		Creator: m.creatorAddr,
		GameId:  gameId,
	}
	if delay > 0 {
		go m.delayPush(msg, delay)
	} else {
		m.e.Push(msg)
	}
}

func (m *MinidiceRound) endRoundOrDelay(gameId string, delay time.Duration) {
	msg := &minidicetypes.MsgEndRound{
		Creator: m.creatorAddr,
		GameId:  gameId,
	}
	if delay > 0 {
		go m.delayPush(msg, delay)
	} else {
		m.e.Push(msg)
	}
}

func (m *MinidiceRound) finalizeRoundOrDelay(gameId string, delay time.Duration) {
	msg := &minidicetypes.MsgFinalizeRound{
		Creator: m.creatorAddr,
		GameId:  gameId,
	}
	if delay > 0 {
		go m.delayPush(msg, delay)
	} else {
		m.e.Push(msg)
	}
}

func (m *MinidiceRound) handleInitGame(events []abci.Event) error {
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		m.startRoundOrDelay(gameId, 0)
	}
	return nil
}

func (m *MinidiceRound) handleStartRound(events []abci.Event) error {
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())

		m.sinceStartsMu.Lock()
		m.sinceStarts[gameId] = time.Now().Unix()
		m.sinceStartsMu.Unlock()

		m.endRoundOrDelay(gameId, time.Duration(m.options.StartRoundInterval)*time.Second)
	}
	return nil
}

func (m *MinidiceRound) handleEndRound(events []abci.Event) error {
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())
		m.finalizeRoundOrDelay(gameId, 0)
	}
	return nil
}

func (m *MinidiceRound) handleFinalizeRound(events []abci.Event) error {
	for _, event := range events {
		gameIdAttr, err := FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())

		m.sinceStartsMu.Lock()
		t := m.sinceStarts[gameId]
		m.sinceStartsMu.Unlock()

		startedIn := time.Now().Unix() - t
		diff := m.options.RoundInterval - int(startedIn)
		m.startRoundOrDelay(gameId, time.Duration(diff)*time.Second)
	}
	return nil
}

func (m *MinidiceRound) FilterRoundEvent(state *tmstate.ABCIResponses) error {
	for _, deliverTx := range state.DeliverTxs {
		m.logger.Debug("FilterRoundEvent", "deliverTx", deliverTx.String())
		if deliverTx.Code != 0 {
			continue
		}
		var events []abci.Event
		var err error
		// Filter Initgame
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeInitGame)
		err = m.handleInitGame(events)
		if err != nil {
			return err
		}
		// Filter StartRound
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeStartRound)
		err = m.handleStartRound(events)
		if err != nil {
			return err
		}
		// Filter EndRound
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeEndRound)
		err = m.handleEndRound(events)
		if err != nil {
			return err
		}
		// Filter FinalizeRound
		events = FindEventsByType(deliverTx.Events, minidicetypes.EventTypeFinalizeRound)
		err = m.handleFinalizeRound(events)
		if err != nil {
			return err
		}
	}
	return nil
}
