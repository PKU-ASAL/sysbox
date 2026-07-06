package api

import "sync"

type TopologyLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newTopologyLocks() *TopologyLocks {
	return &TopologyLocks{locks: map[string]*sync.Mutex{}}
}

func (l *TopologyLocks) Lock(topology string) func() {
	l.mu.Lock()
	mu, ok := l.locks[topology]
	if !ok {
		mu = &sync.Mutex{}
		l.locks[topology] = mu
	}
	l.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}
