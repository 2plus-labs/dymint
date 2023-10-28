package round

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/avast/retry-go"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
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
	logger     log.Logger
	pubsub     *pubsub.Server
	options    *MinidiceOptions
	// tplusClient for query and broadcast tx to tplus chain app
	tplusClient *tplus.TplusClient
	creator     string
	creatorAddr string

	// use to retry call startRound, endRound, finalizeRound
	ctlRoundRetryAttempts uint
	ctlRoundRetryDelay    time.Duration
	ctlRoundRetryMaxDelay time.Duration

	// temp cache round_id will remove later
	currentRound uint32

	// Time diff from started round in seconds
	diffSinceStarted atomic.Int64
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
		ctx:                   ctx,
		cancelFunc:            cancel,
		options:               options,
		pubsub:                pubsub,
		logger:                logger,
		tplusClient:           tplusClient,
		creator:               creator,
		currentRound:          0,
		ctlRoundRetryAttempts: 5,
		ctlRoundRetryDelay:    300 * time.Millisecond,
		ctlRoundRetryMaxDelay: 5 * time.Second,
	}

	creatorAddr, err := m.tplusClient.GetAccountAddress(m.creator)
	if err != nil {
		m.logger.Error("NewMinidiceRound", "creator", creator)
		return nil, err
	}
	logger.Info("minidice round", "creator", creator, "creator addr", creatorAddr)
	m.creatorAddr = creatorAddr

	return m, nil
}

func (m *MinidiceRound) Start() error {
	err := m.tplusClient.StartEventListener()
	if err != nil {
		m.logger.Error("minidice round start", "err", err)
		return err
	}
	m.subscribeAndHandleEvents(m.ctx)

	go m.filterEventGame(EventMinidiceInitGame, QueryMinidiceInitGame, func(raw ctypes.ResultEvent) (any, error) {
		denomVals, ok := raw.Events["InitGame.game_denom"]
		if !ok {
			return MinidiceInitGameData{}, errors.New("failed get denom from events")
		}
		if len(denomVals) == 0 {
			return MinidiceInitGameData{}, errors.New("list game denom is empty")
		}
		return MinidiceInitGameData{Denom: denomVals[0]}, nil
	})

	go m.filterEventGame(EventMinidiceStartRound, QueryMinidiceStartRound, func(raw ctypes.ResultEvent) (any, error) {
		denomVals, ok := raw.Events["StartRound.game_denom"]
		if !ok {
			return MinidiceStartRoundData{}, errors.New("failed get denom from events")
		}
		if len(denomVals) == 0 {
			return MinidiceStartRoundData{}, errors.New("list game denom is empty")
		}
		return MinidiceStartRoundData{Denom: denomVals[0]}, nil
	})

	go m.filterEventGame(EventMinidiceFinalizeRound, QueryMinidiceFinalizeRound, func(raw ctypes.ResultEvent) (any, error) {
		denomVals, ok := raw.Events["FinalizeRound.game_denom"]
		if !ok {
			return MinidiceFinalizeRoundData{}, errors.New("failed get denom from events")
		}
		if len(denomVals) == 0 {
			return MinidiceFinalizeRoundData{}, errors.New("list game denom is empty")
		}
		return MinidiceFinalizeRoundData{Denom: denomVals[0]}, nil
	})

	go m.filterEventGame(EventMinidiceEndRound, QueryMinidiceEndRound, func(raw ctypes.ResultEvent) (any, error) {
		denomVals, ok := raw.Events["EndRound.game_denom"]
		if !ok {
			return MinidiceEndRoundData{}, errors.New("failed get denom from events")
		}
		if len(denomVals) == 0 {
			return MinidiceEndRoundData{}, errors.New("list game denom is empty")
		}
		return MinidiceEndRoundData{Denom: denomVals[0]}, nil
	})

	go func() {
		time.Sleep(1 * time.Second)
		err = m.maybeRecover()
		if err != nil {
			m.logger.Error("minidice round maybeRecover", "err", err)
			panic(err)
		}
	}()
	return nil
}

func (m *MinidiceRound) maybeRecover() error {
	// Recover state of all game active
	activeGames := m.getActiveGames()
	m.logger.Info("maybeRecover", "list active games:", activeGames)
	if len(activeGames) > 0 {
		for _, ag := range activeGames {
			m.logger.Info("active game:", "denom", ag.Denom, "state", ag.State)
			switch ag.State {
			case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED, minidicetypes.RoundState_ROUND_STATE_FINALIZED:
				err := m.startRound(ag.Denom)
				if err != nil {
					m.logger.Error("recover call startRound err", "error", err)
					panic(err)
				}
				data := MinidiceStartRoundData{
					Denom: ag.Denom,
				}
				err = m.pubsub.PublishWithEvents(m.ctx, data,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceStartRound}})
				if err != nil {
					m.logger.Error("pubsub failed", "error", err)
					panic(err)
				}
			case minidicetypes.RoundState_ROUND_STATE_STARTED:
				timeNow := time.Now().UTC().Unix()
				timeEndRound := ag.EndRound
				diffSec := timeNow - timeEndRound
				m.logger.Info("maybeRecover", "diff time", diffSec)
				var err error
				if diffSec > 0 {
					err = m.endRound(ag.Denom)
				} else {
					t := time.NewTicker(time.Duration(-diffSec) * time.Second)
					defer t.Stop()
					<-t.C
					err = m.endRound(ag.Denom)
				}

				if err != nil {
					m.logger.Error("recover call endRound err", "error", err)
					panic(err)
				}
				data := MinidiceEndRoundData{
					Denom: ag.Denom,
				}
				err = m.pubsub.PublishWithEvents(m.ctx, data,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceEndRound}})
				if err != nil {
					m.logger.Error("pubsub failed", "error", err)
					panic(err)
				}
			case minidicetypes.RoundState_ROUND_STATE_ENDED:
				err := m.finalizeRound(ag.Denom)
				if err != nil {
					m.logger.Error("recover call startRound err", "error", err)
					panic(err)
				}
				data := MinidiceFinalizeRoundData{
					Denom: ag.Denom,
				}
				err = m.pubsub.PublishWithEvents(m.ctx, data,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceFinalizeRound}})
				if err != nil {
					m.logger.Error("pubsub failed", "error", err)
					panic(err)
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
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

func (m *MinidiceRound) filterEventGame(eventKey string, query string, parseEventFn func(raw ctypes.ResultEvent) (any, error)) {
	eventsChannel, err := m.tplusClient.SubscribeToEvents(m.ctx, "minidice-round", query)
	if err != nil {
		panic("Error subscribing to events")
	}
	m.logger.Info("subcribed to tplus events", "query", query)
	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info("context done")
			return
		case <-m.tplusClient.EventListenerQuit():
			panic("ws minidice round disconnected")
		case event := <-eventsChannel:
			m.logger.Info("Received event from tplus", event)
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
	m.logger.Info("Received internal minidice init game event", "denom", eventData.Denom)

	err := m.startRound(eventData.Denom)
	if err != nil {
		m.logger.Info("call startRound error", "err", err)
	}
}

func (m *MinidiceRound) startRoundCallback(event pubsub.Message) {
	m.diffSinceStarted.Store(time.Now().Unix())
	eventData := event.Data().(MinidiceStartRoundData)
	m.logger.Info("Received internal start round event", "denom", eventData.Denom)

	info, err := m.tplusClient.GetActiveGame(eventData.Denom)
	if err != nil {
		m.logger.Info("call GetActiveGame failed", "err", err)
		panic(err)
	}

	t := time.NewTicker(time.Duration(m.options.StartRoundInterval) * time.Second)
	defer t.Stop()
	<-t.C
	m.logger.Info("startRoundCallback", "active game", info.String())
	m.logger.Info("startRoundCallback", "time now", time.Now().UTC().Unix())
	err = m.endRound(eventData.Denom)
	if err != nil {
		m.logger.Error("call endRound err", "error", err)
		panic(err)
	}
}

func (m *MinidiceRound) endRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceEndRoundData)
	m.logger.Info("Received internal end round event", "denom", eventData.Denom)
	err := m.finalizeRound(eventData.Denom)
	if err != nil {
		m.logger.Error("call finalizeRound err", "error", err)
	}
}

func (m *MinidiceRound) finalizeRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceFinalizeRoundData)
	m.logger.Info("received internal finalize round event", "denom", eventData.Denom)

	t := m.diffSinceStarted.Load()
	startedIn := time.Now().Unix() - t
	diff := m.options.RoundInterval - int(startedIn)
	if diff > 0 {
		time.Sleep(time.Duration(diff) * time.Second)
	}

	err := m.startRound(eventData.Denom)
	if err != nil {
		m.logger.Error("call startRound err", "error", err)
	}
}

func (m *MinidiceRound) getActiveGames() []*minidicetypes.ActiveGame {
	return m.tplusClient.GetActiveGames(m.ctx)
}

func (m *MinidiceRound) startRound(denom string) error {
	msg := minidicetypes.MsgStartRound{
		Creator: m.creatorAddr,
		Denom:   denom,
	}
	m.logger.Info("MinidiceRound", "broadcast startRound")
	err := retry.Do(func() error {
		txResp, err := m.tplusClient.BroadcastTx(m.creator, &msg)
		if err != nil || txResp.Code != 0 {
			m.logger.Error("broadcast startRound error", "err", err)
			return err
		}
		return nil
	}, retry.Context(m.ctx), retry.LastErrorOnly(true), retry.Delay(m.ctlRoundRetryDelay),
		retry.MaxDelay(m.ctlRoundRetryMaxDelay), retry.Attempts(m.ctlRoundRetryAttempts))
	m.currentRound++
	m.logger.Info("initGameCallback", "current round", m.currentRound)

	return err
}

func (m *MinidiceRound) endRound(denom string) error {
	msg := minidicetypes.MsgEndRound{
		Creator: m.creatorAddr,
		Denom:   denom,
	}
	m.logger.Info("MinidiceRound", "broadcast endRound", msg.String())
	err := retry.Do(func() error {
		txResp, err := m.tplusClient.BroadcastTx(m.creator, &msg)
		if err != nil || txResp.Code != 0 {
			m.logger.Error("broadcast endRound error", "err", err)
			return err
		}
		return nil
	}, retry.Context(m.ctx), retry.LastErrorOnly(true), retry.Delay(m.ctlRoundRetryDelay),
		retry.MaxDelay(m.ctlRoundRetryMaxDelay), retry.Attempts(m.ctlRoundRetryAttempts))

	m.logger.Info("endRoundCallback", "current round", m.currentRound)

	return err
}

func (m *MinidiceRound) finalizeRound(denom string) error {
	msg := minidicetypes.MsgFinalizeRound{
		Creator: m.creatorAddr,
		Denom:   denom,
	}
	m.logger.Info("MinidiceRound", "broadcast finalizeRound")
	err := retry.Do(func() error {
		txResp, err := m.tplusClient.BroadcastTx(m.creator, &msg)
		if err != nil || txResp.Code != 0 {
			m.logger.Error("broadcast finalizeRound error", "err", err)
			return err
		}
		return nil
	}, retry.Context(m.ctx), retry.LastErrorOnly(true), retry.Delay(m.ctlRoundRetryDelay),
		retry.MaxDelay(m.ctlRoundRetryMaxDelay), retry.Attempts(m.ctlRoundRetryAttempts))

	m.logger.Info("finalizeRoundCallback", "current round", m.currentRound)

	return err
}
