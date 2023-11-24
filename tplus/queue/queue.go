package queue

import (
	"sort"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

type MsgInQueue struct {
	Messages []sdk.Msg
	TimeExec int64
}

type Queue struct {
	mu    sync.RWMutex
	elems []MsgInQueue
}

func NewQueue() *Queue {
	return &Queue{
		elems: make([]MsgInQueue, 0),
		mu:    sync.RWMutex{},
	}
}

func (q *Queue) Push(msg MsgInQueue) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.elems = append(q.elems, msg)
}

func (q *Queue) PushBatch(msgs []MsgInQueue) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.elems = append(q.elems, msgs...)
}

func (q *Queue) Pop() (MsgInQueue, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.elems) == 0 {
		return MsgInQueue{}, false
	}

	elem := q.elems[0]
	q.elems = q.elems[1:]

	return elem, true
}

func (q *Queue) PopBatch(maxBatch int) ([]MsgInQueue, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.elems) == 0 {
		return nil, false
	}
	sort.Slice(q.elems, func(i, j int) bool {
		return q.elems[i].TimeExec < q.elems[j].TimeExec
	})

	var elems []MsgInQueue
	if len(q.elems) < maxBatch {
		elems = q.elems
		q.elems = make([]MsgInQueue, 0)
	} else {
		elems = q.elems[:maxBatch]
		q.elems = q.elems[maxBatch:]
	}

	return elems, true
}

func (q *Queue) PopByTime(timeExec int64, maxBatch int) ([]MsgInQueue, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.elems) == 0 {
		return nil, false
	}

	sort.Slice(q.elems, func(i, j int) bool {
		return q.elems[i].TimeExec < q.elems[j].TimeExec
	})

	var elems []MsgInQueue
	idx := 0
	for i, elem := range q.elems {
		if elem.TimeExec <= timeExec {
			elems = append(elems, elem)
			idx = i
		} else {
			break
		}
		if idx >= maxBatch {
			break
		}
	}
	if len(elems) == 0 {
		return nil, false
	}
	q.elems = q.elems[idx+1:]
	return elems, true

}

func (q *Queue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return len(q.elems)
}
