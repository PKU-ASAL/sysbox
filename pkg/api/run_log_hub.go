package api

import "sync"

type RunLogHub struct {
	mu   sync.Mutex
	logs map[string]*Broadcaster
}

func newRunLogHub() *RunLogHub {
	return &RunLogHub{logs: map[string]*Broadcaster{}}
}

func (h *RunLogHub) Writer(runID string) *Broadcaster {
	return h.ensure(runID, false)
}

func (h *RunLogHub) Ensure(runID string, closed bool) *Broadcaster {
	return h.ensure(runID, closed)
}

func (h *RunLogHub) Close(runID string) {
	h.ensure(runID, false).Close()
}

func (h *RunLogHub) ensure(runID string, closed bool) *Broadcaster {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.logs == nil {
		h.logs = map[string]*Broadcaster{}
	}
	b, ok := h.logs[runID]
	if !ok {
		b = &Broadcaster{}
		h.logs[runID] = b
	}
	if closed {
		b.Close()
	}
	return b
}
