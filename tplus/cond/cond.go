package cond

import (
	"context"
	"sync"
)

type Cond struct {
	L       sync.Locker
	m       sync.Mutex
	waiters []chan struct{}
}

func NewCond(l sync.Locker) *Cond {
	return &Cond{
		L: l,
	}
}

func (c *Cond) Broadcast() {
	c.m.Lock()
	defer c.m.Unlock()

	for _, wait := range c.waiters {
		close(wait)
	}
	c.waiters = nil
}

func (c *Cond) Signal() {
	c.m.Lock()
	defer c.m.Unlock()

	if len(c.waiters) == 0 {
		return
	}
	wait := c.waiters[0]
	c.waiters = c.waiters[1:]
	close(wait)
}

func (c *Cond) Wait(ctx context.Context) error {
	wait := make(chan struct{})
	c.m.Lock()
	c.waiters = append(c.waiters, wait)
	c.m.Unlock()

	c.L.Unlock()
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-wait:
	}
	c.L.Lock()
	return err
}
