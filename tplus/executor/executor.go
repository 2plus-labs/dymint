package executor

import (
	"context"
	"time"

	"github.com/avast/retry-go/v4"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/davecgh/go-spew/spew"
	"github.com/dymensionxyz/dymint/log"
	"github.com/dymensionxyz/dymint/tplus"
	"github.com/dymensionxyz/dymint/tplus/queue"
)

type Executor struct {
	logger                log.Logger
	ctx                   context.Context
	client                *tplus.TplusClient
	ctlRoundRetryAttempts uint
	ctlRoundRetryDelay    time.Duration
	ctlRoundRetryMaxDelay time.Duration
	maxBatch              int
	sender                string
	queueExec             *queue.Queue
}

func NewExecutor(ctx context.Context, logger log.Logger, client *tplus.TplusClient, sender string, maxBatch int,
	msgQueue *queue.Queue) *Executor {
	e := &Executor{
		ctx:                   ctx,
		logger:                logger,
		client:                client,
		ctlRoundRetryAttempts: 5,
		ctlRoundRetryDelay:    500 * time.Millisecond,
		ctlRoundRetryMaxDelay: 5 * time.Second,
		queueExec:             msgQueue,
		maxBatch:              maxBatch,
		sender:                sender,
	}
	return e
}

func (e *Executor) Serve() error {
	isExecuted := false
	t := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-e.ctx.Done():
			return nil
		case <-t.C:
			e.logger.Info("executor: ", "sender", e.sender, "time", time.Now().Unix())
			if !isExecuted {
				isExecuted = true
				err := e.Broadcast(e.ctx)
				if err != nil {
					e.logger.Error("executor: ", "sender", e.sender, "broadcast tx error", "err", err)
					isExecuted = false
					return err
				}
				isExecuted = false
			}
		}
	}
}

func (e *Executor) PushBatch(msgs []queue.MsgInQueue) {
	e.queueExec.PushBatch(msgs)
}

func (e *Executor) Broadcast(ctx context.Context) error {
	items, found := e.queueExec.PopByTime(time.Now().Unix(), e.maxBatch)
	if !found || len(items) == 0 {
		e.logger.Info("executor: ", "sender", e.sender, "no msgs found", "found", found, "len", len(items))
		return nil
	}
	//e.logger.Info("executor: ", "sender", e.sender, "broadcast msgs", len(items))
	err := retry.Do(func() error {
		msgs := make([]sdk.Msg, 0)
		for _, msgInQueue := range items {
			msgs = append(msgs, msgInQueue.Messages...)
		}
		spew.Dump(msgs)
		txResp, err := e.client.BroadcastTx(e.sender, msgs...)
		if err != nil || txResp.Code != 0 {
			e.logger.Error("broadcast tx error", "err", err)
			//if txResp.Code == 32 {
			//accNum, accSeq, err := e.client.GetAccountNumberSequence(e.sender)
			//if err != nil {
			//	e.logger.Error("get account error", "err", err)
			//	return err
			//}
			//e.logger.Error("executor: ", "sender", e.sender, "retrying with", "accNum", accNum, "accSeq", accSeq)
			//e.client.Factory.WithSequence(accSeq + 1).WithAccountNumber(accNum)
			//}
			return err
		}
		e.logger.Info("executor: ", "sender", e.sender, "broadcast tx success", "txHash", txResp.TxHash)
		return nil
	}, retry.Context(ctx), retry.LastErrorOnly(true), retry.Delay(e.ctlRoundRetryDelay),
		retry.MaxDelay(e.ctlRoundRetryMaxDelay), retry.Attempts(e.ctlRoundRetryAttempts))
	if err != nil {
		e.logger.Error("broadcast tx error in last retry", "err", err)
		return err
	}
	return nil
}
