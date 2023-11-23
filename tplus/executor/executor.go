package executor

import (
	"context"
	"time"

	"github.com/avast/retry-go"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
	"github.com/dymensionxyz/dymint/tplus/queue"
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
}

func NewExecutor(
	ctx context.Context,
	logger log.Logger,
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
	}
	go e.serve(ctx)
	return e
}

func (e *Executor) Push(msgs ...sdk.Msg) {
	for _, msg := range msgs {
		e.msgs.Push(msg)
	}
}

func (e *Executor) serve(ctx context.Context) error {
	for {
		items, err := e.msgs.Pop(ctx, e.maxBatch)
		if err != nil {
			return err
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
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}
