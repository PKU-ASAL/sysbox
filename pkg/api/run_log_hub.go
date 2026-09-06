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

// Reopen replaces any existing broadcaster for runID with a fresh, open one.
// It is used when a run id is reused for a new attempt (e.g. an apply retry
// after a terminal run) so the new attempt's logs are not dropped into a
// broadcaster already closed by the prior attempt.
func (h *RunLogHub) Reopen(runID string) *Broadcaster {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.logs == nil {
		h.logs = map[string]*Broadcaster{}
	}
	b := &Broadcaster{}
	h.logs[runID] = b
	return b
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
