package api

import (
	"context"
	"sync"
)

type TopologyLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func newTopologyLocks() *TopologyLocks {
	return &TopologyLocks{locks: map[string]chan struct{}{}}
}

func (l *TopologyLocks) Lock(topology string) func() {
	unlock, _ := l.LockContext(context.Background(), topology)
	return unlock
}

func (l *TopologyLocks) LockContext(ctx context.Context, topology string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	mu, ok := l.locks[topology]
	if !ok {
		mu = make(chan struct{}, 1)
		l.locks[topology] = mu
	}
	l.mu.Unlock()
	select {
	case mu <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-mu }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
