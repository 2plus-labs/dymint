package round

import (
	"context"
	"time"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/avast/retry-go"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
	"github.com/dymensionxyz/dymint/tplus/queue"
	abci "github.com/tendermint/tendermint/abci/types"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
)

type eventData struct {
	events map[string][]abci.Event
}

type Executor struct {
	logger                log.Logger
	ctx                   context.Context
	client                *tplus.TplusClient
	msgs                  *queue.Queue
	ctlRoundRetryAttempts uint
	ctlRoundRetryDelay    time.Duration
	ctlRoundRetryMaxDelay time.Duration
	maxBatch              int
	sender                string
	eventsCh              chan eventData
	eventsCap             int
	m                     *MinidiceRound
}

func NewExecutor(
	ctx context.Context,
	logger log.Logger,
	m *MinidiceRound,
	client *tplus.TplusClient,
	sender string,
	maxBatch int,
	eventsCap int,
) *Executor {
	e := &Executor{
		ctx:                   ctx,
		logger:                logger,
		client:                client,
		ctlRoundRetryAttempts: 5,
		ctlRoundRetryDelay:    300 * time.Millisecond,
		ctlRoundRetryMaxDelay: 5 * time.Second,
		msgs:                  &queue.Queue{},
		maxBatch:              maxBatch,
		sender:                sender,
		eventsCh:              make(chan eventData, eventsCap),
		eventsCap:             eventsCap,
		m:                     m,
	}
	go e.broadcastMsgsLoop(ctx)
	go e.handleEventsLoop(ctx)
	return e
}

func (e *Executor) Push(msgs ...sdk.Msg) {
	for _, msg := range msgs {
		e.msgs.Push(msg)
	}
}

func (e *Executor) broadcastMsgsLoop(ctx context.Context) {
	for {
		items, err := e.msgs.Pop(ctx, e.maxBatch)
		if err != nil {
			e.logger.Error("executor pop failed: ", "err", err)
			panic(err)
		}
		e.logger.Info("executor: ", "sender", e.sender, "broadcast msgs", len(items))
		err = retry.Do(func() error {
			txResp, err := e.client.BroadcastTx(e.sender, items...)
			if err != nil || txResp.Code != 0 {
				e.logger.Error("broadcast tx error", "err", err)
				return err
			}
			return nil
		}, retry.Context(ctx), retry.LastErrorOnly(true), retry.Delay(e.ctlRoundRetryDelay),
			retry.MaxDelay(e.ctlRoundRetryMaxDelay), retry.Attempts(e.ctlRoundRetryAttempts))
		if err != nil {
			e.logger.Error("broadcast tx error in last retry", "err", err)
			panic(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (e *Executor) handleEventsLoop(ctx context.Context) {
	for data := range e.eventsCh {
		for eventKey := range data.events {
			switch eventKey {
			case minidicetypes.EventTypeInitGame:
				e.m.handleInitGame(data.events[eventKey])
			case minidicetypes.EventTypeStartRound:
				e.m.handleStartRound(data.events[eventKey])
			case minidicetypes.EventTypeEndRound:
				e.m.handleEndRound(data.events[eventKey])
			case minidicetypes.EventTypeFinalizeRound:
				e.m.handleFinalizeRound(data.events[eventKey])
			}
		}
	}
}

func (e *Executor) publishResponses(ctx context.Context, responses *tmstate.ABCIResponses) error {
	roundEvents := e.filterRoundEvents(responses)
	select {
	case e.eventsCh <- eventData{events: roundEvents}:
		return nil
	case <-ctx.Done():
		return nil
	}
}

func (e *Executor) filterRoundEvents(responses *tmstate.ABCIResponses) map[string][]abci.Event {
	roundEvents := map[string][]abci.Event{}
	for _, deliverTx := range responses.DeliverTxs {
		if deliverTx.Code != 0 {
			continue
		}
		// Filter Initgame
		eventsInitGame := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeInitGame)
		roundEvents[minidicetypes.EventTypeInitGame] = eventsInitGame

		// Filter StartRound
		eventsStart := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeStartRound)
		roundEvents[minidicetypes.EventTypeStartRound] = eventsStart

		// Filter EndRound
		eventsEnd := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeEndRound)
		roundEvents[minidicetypes.EventTypeEndRound] = eventsEnd

		// Filter FinalizeRound
		eventsFinalize := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeFinalizeRound)
		roundEvents[minidicetypes.EventTypeFinalizeRound] = eventsFinalize
	}
	return roundEvents
}
