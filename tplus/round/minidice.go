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
	abci "github.com/tendermint/tendermint/abci/types"
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
	ctx          context.Context
	cancelFunc   context.CancelFunc
	logger       log.Logger
	eventsFilter *EventsFilter
	options      *MinidiceOptions
	// tplusClient for query and broadcast tx to tplus chain app
	tplusClient *tplus.TplusClient
	creator     string
	creatorAddr string

	e *Executor

	// Time diff from started round in seconds by denom
	sinceStartsMu sync.Mutex
	sinceStarts   map[string]int64
}

func NewMinidiceRound(
	tplusConfig *tplus.Config,
	options *MinidiceOptions,
	logger log.Logger,
	eventsFilter *EventsFilter,
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
		eventsFilter:  eventsFilter,
		logger:        logger,
		tplusClient:   tplusClient,
		creator:       tplusConfig.AccountName,
		sinceStarts:   map[string]int64{},
		sinceStartsMu: sync.Mutex{},
	}

	creatorAddr, err := m.tplusClient.GetAccountAddress(m.creator)
	if err != nil {
		m.logger.Error("NewMinidiceRound", "creator", m.creator)
		return nil, err
	}
	logger.Info("minidice round", "creator", m.creator, "creator addr", creatorAddr)
	m.creatorAddr = creatorAddr

	e := NewExecutor(ctx, logger, m, eventsFilter, tplusClient, m.creator, 5)
	m.e = e

	return m, nil
}

func (m *MinidiceRound) Start() error {
	m.e.Run(m.ctx)

	activeGames := m.getActiveGames()
	if len(activeGames) > 0 {
		go m.recoverActiveGames(activeGames)
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

func (m *MinidiceRound) recoverActiveGames(activeGames []*minidicetypes.ActiveGame) {
	for _, ag := range activeGames {
		switch ag.State {
		case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED:
			// Do nothing
		case minidicetypes.RoundState_ROUND_STATE_FINALIZED:
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
			if diffSec >= 0 {
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
		gameIdAttr, err := tplus.FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
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
		gameIdAttr, err := tplus.FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
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
		gameIdAttr, err := tplus.FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
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
		gameIdAttr, err := tplus.FindAttributeByKey(event, minidicetypes.AttributeKeyGameID)
		if err != nil {
			return fmt.Errorf("error while getting mini-dice game id: %s", err)
		}
		gameId := string(gameIdAttr.GetValue())

		m.sinceStartsMu.Lock()
		t := m.sinceStarts[gameId]
		m.sinceStartsMu.Unlock()

		startedIn := time.Now().Unix() - t
		diff := m.options.RoundInterval - int(startedIn)
		if diff > 0 {
			m.startRoundOrDelay(gameId, time.Duration(diff)*time.Second)
		} else {
			m.startRoundOrDelay(gameId, 0)
		}

	}
	return nil
}
