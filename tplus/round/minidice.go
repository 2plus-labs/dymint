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
		StartRoundInterval:    45,
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
	err := m.tplusClient.StartEventListener()
	if err != nil {
		m.logger.Error("minidice round start", "err", err)
		return err
	}
	// m.activeGames = m.getActiveGames()
	go m.run(m.ctx)
	go m.filterEventInitGame()
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
	eventsChannel, err := m.tplusClient.SubscribeToEvents(m.ctx, "minidice-round", minidicetypes.EventTypeInitGame)
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
			m.logger.Info("Received event from tplus")
			if event.Query != minidicetypes.EventTypeInitGame {
				m.logger.Info("Ignoring event. Type not supported", "event", event)
				continue
			}

			eventData, err := m.getEventData(EventMinidiceInitGame, event)
			if err != nil {
				panic(err)
			}
			err = m.pubsub.PublishWithEvents(m.ctx, eventData, map[string][]string{EventMinidiceTypekey: {EventMinidiceInitGame}})
			if err != nil {
				panic(err)
			}
		}
	}
}

func (m *MinidiceRound) getEventData(eventType string, raw ctypes.ResultEvent) (any, error) {
	switch eventType {
	case EventMinidiceInitGame:
		return nil, nil
	}
	return nil, fmt.Errorf("event type %s not recognized", eventType)
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
	m.logger.Debug("Received minidice init game event", "eventData", eventData)
	err := m.startRound(eventData.Denom)
	if err != nil {
		m.logger.Debug("initGameCallback", "err", err)
	}
}

func (m *MinidiceRound) startRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceStartRoundData)
	m.logger.Debug("Received start round event", "eventData", eventData)

	t := time.NewTicker(time.Duration(m.options.StartRoundInterval) * time.Second)
	<-t.C
	err := m.endRound(eventData.Denom)
	if err != nil {
		t.Stop()
	}
}

func (m *MinidiceRound) endRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceEndRoundData)
	m.logger.Debug("Received end round event", "eventData", eventData)

	t := time.NewTicker(time.Duration(m.options.EndRoundInterval) * time.Second)
	<-t.C
	err := m.finalizeRound(eventData.Denom)
	if err != nil {
		t.Stop()
	}
}

func (m *MinidiceRound) finalizeRoundCallback(event pubsub.Message) {
	eventData := event.Data().(MinidiceFinalizeRoundData)
	m.logger.Debug("Received finalize round event", "eventData", eventData)

	t := time.NewTicker(time.Duration(m.options.FinalizeRoundInterval) * time.Second)
	<-t.C
	err := m.startRound(eventData.Denom)
	if err != nil {
		t.Stop()
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
	if err != nil && txResp.Code != 0 {
		m.logger.Error("finalizeRound", "err", err)
		return err
	}
	return nil
}
