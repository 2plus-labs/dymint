package round

import (
	"context"
	"fmt"
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
	// All interval is by sec
	StartRoundInterval    int
	EndRoundInterval      int
	FinalizeRoundInterval int
}

func DefaultOptions() *MinidiceOptions {
	return &MinidiceOptions{
		StartRoundInterval:    62,
		EndRoundInterval:      12,
		FinalizeRoundInterval: 3,
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
	activeGames []*minidicetypes.ActiveGame
	creator     string
	creatorAddr string
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
		ctx:         ctx,
		cancelFunc:  cancel,
		options:     options,
		pubsub:      pubsub,
		logger:      logger,
		tplusClient: tplusClient,
		creator:     creator,
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
	err := m.maybeRecover()
	if err != nil {
		m.logger.Error("minidice round maybeRecover", "err", err)
		return err
	}
	err = m.tplusClient.StartEventListener()
	if err != nil {
		m.logger.Error("minidice round start", "err", err)
		return err
	}
	go m.run(m.ctx)
	go m.filterEventInitGame()
	return nil
}

func (m *MinidiceRound) maybeRecover() error {
	// Recover state of all game active
	m.activeGames = m.getActiveGames()
	if len(m.activeGames) > 0 {
		m.logger.Info("run recover")
		for _, ag := range m.activeGames {
			m.logger.Info("maybeRecover", "active game", ag.String())
			switch ag.State {
			case minidicetypes.RoundState_ROUND_STATE_NOT_STARTED:
				eventData := MinidiceStartRoundData{
					Denom: ag.Denom,
				}
				err := m.pubsub.PublishWithEvents(m.ctx, eventData,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceStartRound}})
				if err != nil {
					m.logger.Error("maybeRecover RoundState_ROUND_STATE_NOT_STARTED pubsub error", "error", err)
					return err
				}
			case minidicetypes.RoundState_ROUND_STATE_STARTED:
				eventData := MinidiceEndRoundData{
					Denom: ag.Denom,
				}
				err := m.pubsub.PublishWithEvents(m.ctx, eventData,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceEndRound}})
				if err != nil {
					m.logger.Error("maybeRecover RoundState_ROUND_STATE_STARTED pubsub error", "error", err)
					return err
				}
			case minidicetypes.RoundState_ROUND_STATE_ENDED:
				eventData := MinidiceFinalizeRoundData{
					Denom: ag.Denom,
				}
				err := m.pubsub.PublishWithEvents(m.ctx, eventData,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceFinalizeRound}})
				if err != nil {
					m.logger.Error("maybeRecover RoundState_ROUND_STATE_ENDED pubsub error", "error", err)
					return err
				}
			case minidicetypes.RoundState_ROUND_STATE_FINALIZED:
				eventData := MinidiceStartRoundData{
					Denom: ag.Denom,
				}
				err := m.pubsub.PublishWithEvents(m.ctx, eventData,
					map[string][]string{EventMinidiceTypekey: {EventMinidiceStartRound}})
				if err != nil {
					m.logger.Error("maybeRecover RoundState_ROUND_STATE_FINALIZED pubsub error", "error", err)
					return err
				}
			}
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

func (m *MinidiceRound) filterEventInitGame() {
	m.logger.Info("minidice round filterEventInitGame")
	query := fmt.Sprintf("InitGame.creator='%s'", m.creatorAddr)
	eventsChannel, err := m.tplusClient.SubscribeToEvents(m.ctx, "minidice-round", query)
	if err != nil {
		panic("Error subscribing to events")
	}
	m.logger.Info("subcribed to tplus EventTypeInitGame")
	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info("filterEventInitGame context done")
			return
		case <-m.tplusClient.EventListenerQuit():
			panic("ws minidice round disconnected")
		case event := <-eventsChannel:
			m.logger.Info("Received event from tplus", event)
			eventData, err := m.getEventData(event)
			if err != nil {
				panic(err)
			}
			// m.logger.Info("filterEventInitGame", "publish event", eventData)
			err = m.pubsub.PublishWithEvents(m.ctx, eventData, map[string][]string{EventMinidiceTypekey: {EventMinidiceInitGame}})
			if err != nil {
				panic(err)
			}
		}
	}
}

func (m *MinidiceRound) getEventData(raw ctypes.ResultEvent) (MinidiceInitGameData, error) {
	denomVals, ok := raw.Events["InitGame.game_denom"]
	if !ok {
		return MinidiceInitGameData{}, fmt.Errorf("failed get InitGame.game_denom from events")
	}
	return MinidiceInitGameData{Denom: denomVals[0]}, nil
}

func (m *MinidiceRound) run(ctx context.Context) {
	m.logger.Info("minidice round handle events internal")
	clientID := "MinidiceRound"

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceInitGameQuery, m.initGameCallback, m.logger)

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceStartRoundQuery, m.startRoundCallback, m.logger)

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceEndRoundQuery, m.endRoundCallback, m.logger)

	go utils.SubscribeAndHandleEvents(ctx, m.pubsub, clientID, EventMinidiceFinalizeRoundQuery, m.finalizeRoundCallback, m.logger)

}

func (m *MinidiceRound) initGameCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceInitGameData)
	m.logger.Info("Received minidice init game event", "eventData", eventData)

	err := m.startRound(eventData.Denom)
	if err != nil {
		m.logger.Info("initGameCallback", "err", err)
	}

	startRoundEvent := MinidiceStartRoundData(eventData)
	err = m.pubsub.PublishWithEvents(m.ctx, startRoundEvent, map[string][]string{EventMinidiceTypekey: {EventMinidiceStartRound}})
	if err != nil {
		m.logger.Error("initGameCallback pubsub failed", "error", err)
		panic(err)
	}
}

func (m *MinidiceRound) startRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceStartRoundData)
	m.logger.Info("Received start round event", "eventData", eventData)

	info, err := m.tplusClient.GetActiveGame(eventData.Denom)
	if err != nil {
		m.logger.Info("startRoundCallback GetActiveGame failed", "err", err)
		panic(err)
	}

	m.logger.Info("startRoundCallback", "active game", info.String())
	m.logger.Info("startRoundCallback", "time now", time.Now().Unix())

	t := time.NewTicker(time.Duration(m.options.StartRoundInterval) * time.Second)
	defer t.Stop()
	<-t.C
	err = m.endRound(eventData.Denom)
	if err != nil {
		m.logger.Error("startRoundCallback endRound err", "error", err)
	}

	endRoundEvent := MinidiceEndRoundData(eventData)
	err = m.pubsub.PublishWithEvents(m.ctx, endRoundEvent, map[string][]string{EventMinidiceTypekey: {EventMinidiceEndRound}})
	if err != nil {
		m.logger.Error("startRoundCallback pubsub failed", "error", err)
		panic(err)
	}
}

func (m *MinidiceRound) endRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceEndRoundData)
	m.logger.Info("Received end round event", "eventData", eventData)

	t := time.NewTicker(time.Duration(m.options.EndRoundInterval) * time.Second)
	defer t.Stop()
	<-t.C
	err := m.finalizeRound(eventData.Denom)
	if err != nil {
		m.logger.Error("endRoundCallback finalizeRound err", "error", err)
	}

	finalizeRoundEvent := MinidiceFinalizeRoundData(eventData)
	err = m.pubsub.PublishWithEvents(m.ctx, finalizeRoundEvent, map[string][]string{EventMinidiceTypekey: {EventMinidiceFinalizeRound}})
	if err != nil {
		m.logger.Error("endRoundCallback pubsub failed", "err", err)
		panic(err)
	}
}

func (m *MinidiceRound) finalizeRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceFinalizeRoundData)
	m.logger.Info("Received finalize round event", "eventData", eventData)

	t := time.NewTicker(time.Duration(m.options.FinalizeRoundInterval) * time.Second)
	defer t.Stop()
	<-t.C
	err := m.startRound(eventData.Denom)
	if err != nil {
		m.logger.Error("finalizeRoundCallback startRound err", "error", err)
	}
	startRoundEvent := MinidiceStartRoundData(eventData)
	err = m.pubsub.PublishWithEvents(m.ctx, startRoundEvent, map[string][]string{EventMinidiceTypekey: {EventMinidiceStartRound}})
	if err != nil {
		m.logger.Error("finalizeRoundCallback pubsub failed", "err", err)
	}
}

func (m *MinidiceRound) getActiveGames() []*minidicetypes.ActiveGame {
	return m.tplusClient.GetActiveGames(context.Background())
}

func (m *MinidiceRound) startRound(denom string) error {
	msg := minidicetypes.MsgStartRound{
		Creator: m.creatorAddr,
		Denom:   denom,
	}
	txResp, err := m.tplusClient.BroadcastTx(m.creator, &msg)
	if err != nil || txResp.Code != 0 {
		m.logger.Error("startRound", "err", err)
		return err
	}
	return nil
}

func (m *MinidiceRound) endRound(denom string) error {
	msg := minidicetypes.MsgEndRound{
		Creator: m.creatorAddr,
		Denom:   denom,
	}
	txResp, err := m.tplusClient.BroadcastTx(m.creator, &msg)
	if err != nil || txResp.Code != 0 {
		m.logger.Error("endRound", "err", err)
		return err
	}
	return nil
}

func (m *MinidiceRound) finalizeRound(denom string) error {
	msg := minidicetypes.MsgFinalizeRound{
		Creator: m.creatorAddr,
		Denom:   denom,
	}
	txResp, err := m.tplusClient.BroadcastTx(m.creator, &msg)
	if err != nil || txResp.Code != 0 {
		m.logger.Error("finalizeRound", "err", err)
		return err
	}
	return nil
}
