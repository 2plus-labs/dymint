package round

import (
	"context"
	"errors"
	"sync"
	"time"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/avast/retry-go"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
	"github.com/dymensionxyz/dymint/tplus/executor"
	"github.com/dymensionxyz/dymint/utils"
	"github.com/tendermint/tendermint/libs/pubsub"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
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

	e *executor.Executor

	logger  log.Logger
	pubsub  *pubsub.Server
	options *MinidiceOptions

	tplusClient *tplus.TplusClient

	from     string
	fromAddr string

	// use to retry call startRound, endRound, finalizeRound
	ctlRoundRetryAttempts uint
	ctlRoundRetryDelay    time.Duration
	ctlRoundRetryMaxDelay time.Duration

	// Time diff from started round in seconds by denom
	diffSinceStartMu sync.RWMutex
	diffSinceStarts  map[string]int64
}

func NewMinidiceRound(
	tplusConfig *tplus.Config,
	options *MinidiceOptions,
	logger log.Logger,
	pubsub *pubsub.Server,
	from string,
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
		ctx:                   ctx,
		cancelFunc:            cancel,
		options:               options,
		pubsub:                pubsub,
		logger:                logger,
		tplusClient:           tplusClient,
		from:                  from,
		ctlRoundRetryAttempts: 5,
		ctlRoundRetryDelay:    300 * time.Millisecond,
		ctlRoundRetryMaxDelay: 5 * time.Second,
		diffSinceStarts:       map[string]int64{},
		diffSinceStartMu:      sync.RWMutex{},
	}

	fromAddr, err := m.tplusClient.GetAccountAddress(m.from)
	if err != nil {
		m.logger.Error("get account address failed", "from", from)
		return nil, err
	}
	logger.Info("minidice round", "from", from, "from addr", fromAddr)
	m.fromAddr = fromAddr

	e := executor.NewExecutor(ctx, logger, tplusClient, m.from, 10)
	m.e = e

	return m, nil
}

func (m *MinidiceRound) Start() error {
	err := m.tplusClient.StartEventListener()
	if err != nil {
		m.logger.Error("minidice round start", "err", err)
		return err
	}

	m.subscribeAndHandleEvents(m.ctx)
	m.filterEventGames(m.ctx)

	activeGames := m.getActiveGames()
	m.logger.Info("minidice round start: ", "num active games", len(activeGames))
	if len(activeGames) > 0 {
		m.recoverActiveGame(activeGames)
	}

	return nil
}

func (m *MinidiceRound) recoverActiveGame(activeGames []*minidicetypes.ActiveGame) {
	for _, ag := range activeGames {
		switch ag.State {
		case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED, minidicetypes.RoundState_ROUND_STATE_FINALIZED:
			msg := &minidicetypes.MsgStartRound{
				GameId:  ag.GameId,
				Creator: m.fromAddr,
			}
			m.e.Push(msg)
		case minidicetypes.RoundState_ROUND_STATE_STARTED:
			timeNow := time.Now().UTC().Unix()
			timeEndRound := ag.EndRound
			diffSec := timeNow - timeEndRound
			msg := &minidicetypes.MsgEndRound{
				Creator: m.fromAddr,
				GameId:  ag.GameId,
			}
			if diffSec > 0 {
				m.e.Push(msg)
			} else {
				t := time.NewTicker(time.Duration(-diffSec) * time.Second)
				defer t.Stop()
				<-t.C
				m.e.Push(msg)
			}
		case minidicetypes.RoundState_ROUND_STATE_ENDED:
			msg := &minidicetypes.MsgFinalizeRound{
				GameId:  ag.GameId,
				Creator: m.fromAddr,
			}
			m.e.Push(msg)
		}
	}
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

func (m *MinidiceRound) filterEventGames(ctx context.Context) {
	// Listen event InitGame
	go m.filterEventGame(ctx, EventMinidiceInitGame, QueryMinidiceInitGame, func(raw ctypes.ResultEvent) (any, error) {
		gameIdVals, ok := raw.Events["InitGame.game_id"]
		if !ok {
			return MinidiceInitGameData{}, errors.New("failed get game_id from events")
		}
		if len(gameIdVals) == 0 {
			return MinidiceInitGameData{}, errors.New("list game game_id is empty")
		}
		return MinidiceInitGameData{GameId: gameIdVals[0]}, nil
	})

	// Listen event StartRound
	go m.filterEventGame(ctx, EventMinidiceStartRound, QueryMinidiceStartRound, func(raw ctypes.ResultEvent) (any, error) {
		gameIdVals, ok := raw.Events["StartRound.game_id"]
		if !ok {
			return MinidiceStartRoundData{}, errors.New("failed get game_id from events")
		}
		if len(gameIdVals) == 0 {
			return MinidiceStartRoundData{}, errors.New("list game game_id is empty")
		}
		return MinidiceStartRoundData{GameId: gameIdVals[0]}, nil
	})

	// Listen event EndRound
	go m.filterEventGame(ctx, EventMinidiceEndRound, QueryMinidiceEndRound, func(raw ctypes.ResultEvent) (any, error) {
		gameIdVals, ok := raw.Events["EndRound.game_id"]
		if !ok {
			return MinidiceEndRoundData{}, errors.New("failed get game_id from events")
		}
		if len(gameIdVals) == 0 {
			return MinidiceEndRoundData{}, errors.New("list game game_id is empty")
		}
		return MinidiceEndRoundData{GameId: gameIdVals[0]}, nil
	})

	// Listen event FinalizeRound
	go m.filterEventGame(ctx, EventMinidiceFinalizeRound, QueryMinidiceFinalizeRound, func(raw ctypes.ResultEvent) (any, error) {
		gameIdVals, ok := raw.Events["FinalizeRound.game_id"]
		if !ok {
			return MinidiceFinalizeRoundData{}, errors.New("failed get game_id from events")
		}
		if len(gameIdVals) == 0 {
			return MinidiceFinalizeRoundData{}, errors.New("list game game_id is empty")
		}
		return MinidiceFinalizeRoundData{GameId: gameIdVals[0]}, nil
	})
}

func (m *MinidiceRound) filterEventGame(ctx context.Context, eventKey string, query string, parseEventFn func(raw ctypes.ResultEvent) (any, error)) {
	eventsChannel, err := m.tplusClient.SubscribeToEvents(m.ctx, "minidice-round", query)
	if err != nil {
		panic("Error subscribing to events")
	}
	m.logger.Info("subcribed to tplus events", "query", query)
	for {
		select {
		case <-ctx.Done():
			m.logger.Info("context done")
			return
		case <-m.tplusClient.EventListenerQuit():
			panic("ws minidice round disconnected")
		case event := <-eventsChannel:
			m.logger.Debug("Received event from tplus", event)
			eventData, err := parseEventFn(event)
			if err != nil {
				panic(err)
			}
			err = m.pubsub.PublishWithEvents(m.ctx, eventData, map[string][]string{EventMinidiceTypekey: {eventKey}})
			if err != nil {
				panic(err)
			}
		}
	}
}

func (m *MinidiceRound) subscribeAndHandleEvents(ctx context.Context) {
	m.logger.Info("minidice round handle events internal")
	clientID := "MinidiceRound"
	outCapacity := 100

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceInitGameQuery, m.initGameCallback, m.logger, outCapacity)

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceStartRoundQuery, m.startRoundCallback, m.logger, outCapacity)

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceEndRoundQuery, m.endRoundCallback, m.logger, outCapacity)

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceFinalizeRoundQuery, m.finalizeRoundCallback, m.logger, outCapacity)
}

func (m *MinidiceRound) initGameCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceInitGameData)
	m.logger.Info("Received internal minidice init game event", "game_id", eventData.GameId)

	m.startRound(eventData.GameId)
}

func (m *MinidiceRound) startRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceStartRoundData)
	m.logger.Info("Received internal start round event", "game_id", eventData.GameId)

	m.diffSinceStartMu.Lock()
	m.diffSinceStarts[eventData.GameId] = time.Now().Unix()
	m.diffSinceStartMu.Unlock()

	t := time.NewTicker(time.Duration(m.options.StartRoundInterval) * time.Second)
	defer t.Stop()
	<-t.C
	m.endRound(eventData.GameId)
}

func (m *MinidiceRound) endRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceEndRoundData)
	m.logger.Info("Received internal end round event", "denom", eventData.GameId)
	m.finalizeRound(eventData.GameId)
}

func (m *MinidiceRound) finalizeRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceFinalizeRoundData)
	m.logger.Info("received internal finalize round event", "denom", eventData.GameId)

	m.diffSinceStartMu.RLock()
	t := m.diffSinceStarts[eventData.GameId]
	m.diffSinceStartMu.RUnlock()

	startedIn := time.Now().Unix() - t
	diff := m.options.RoundInterval - int(startedIn)
	if diff > 0 {
		time.Sleep(time.Duration(diff) * time.Second)
	}

	m.startRound(eventData.GameId)
}

func (m *MinidiceRound) getActiveGames() []*minidicetypes.ActiveGame {
	return m.tplusClient.GetActiveGames(m.ctx)
}

func (m *MinidiceRound) startRound(gameId string) {
	msg := &minidicetypes.MsgStartRound{
		Creator: m.fromAddr,
		GameId:  gameId,
	}
	m.logger.Info("send start round msg", "fromAddr", m.fromAddr, "game_id", gameId)
	m.e.Push(msg)
}

func (m *MinidiceRound) endRound(gameId string) {
	msg := &minidicetypes.MsgEndRound{
		Creator: m.fromAddr,
		GameId:  gameId,
	}
	m.logger.Info("send end round msg", "fromAddr", m.fromAddr, "game_id", gameId)
	m.e.Push(msg)
}

func (m *MinidiceRound) finalizeRound(gameId string) {
	msg := &minidicetypes.MsgFinalizeRound{
		Creator: m.fromAddr,
		GameId:  gameId,
	}
	m.logger.Info("send finalize round msg", "fromAddr", m.fromAddr, "game_id", gameId)
	m.e.Push(msg)
}
