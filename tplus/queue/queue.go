package queue

import (
	"context"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/dymensionxyz/dymint/tplus/cond"
)

// Queue is a thread-safe queue.
//
// Unlike a Go channel, Queue doesn't have any constraints on how many
// elements can be in the queue.
type Queue struct {
	mu    sync.Mutex
	elems []sdk.Msg
	wait  *cond.Cond
}

// Push places elem at the back of the queue.
func (q *Queue) Push(elem sdk.Msg) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.init()
	q.elems = append(q.elems, elem)
	q.wait.Signal()
}

// It blocks if the queue is empty.
// It returns an error if the passed-in context is canceled.
func (q *Queue) Pop(ctx context.Context, maxBatch int) (elems []sdk.Msg, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.init()
	for len(q.elems) == 0 {
		if err = q.wait.Wait(ctx); err != nil {
			return
		}
	}
	if len(q.elems) <= maxBatch {
		elems = q.elems[:]
		q.elems = []sdk.Msg{}
		return
	} else {
		elems = q.elems[:maxBatch]
		q.elems = q.elems[maxBatch+1:]
		return
	}
}

// init initializes the queue.
//
// REQUIRES: q.mu is held
func (q *Queue) init() {
	if q.wait == nil {
		q.wait = cond.NewCond(&q.mu)
	}
}
