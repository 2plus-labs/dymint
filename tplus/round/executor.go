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
)

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
	m                     *MinidiceRound
	eventsFilter          *EventsFilter
}

func NewExecutor(
	ctx context.Context,
	logger log.Logger,
	m *MinidiceRound,
	eventsFilter *EventsFilter,
	client *tplus.TplusClient,
	sender string,
	maxBatch int,
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
		m:                     m,
		eventsFilter:          eventsFilter,
	}
	return e
}

func (e *Executor) Run(ctx context.Context) {
	go e.broadcastMsgsLoop(ctx)
	go e.handleEventsLoop(ctx)
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
	e.eventsFilter.Run(ctx, func(events map[string][]abci.Event) {
		for eventKey := range events {
			switch eventKey {
			case minidicetypes.EventTypeInitGame:
				e.m.handleInitGame(events[eventKey])
			case minidicetypes.EventTypeStartRound:
				e.m.handleStartRound(events[eventKey])
			case minidicetypes.EventTypeEndRound:
				e.m.handleEndRound(events[eventKey])
			case minidicetypes.EventTypeFinalizeRound:
				e.m.handleFinalizeRound(events[eventKey])
			}
		}
	})
}
