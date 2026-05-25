package api

import (
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type consoleSessionHub struct {
	mu       sync.RWMutex
	sessions map[string]*consoleSessionState
}

type consoleSessionState struct {
	session controlplane.ConsoleSession
	request controlplane.ConsoleRequest
	browser chan *wsPeer
	agent   chan *wsPeer
}

type wsPeer struct {
	conn *websocket.Conn
}

func newConsoleSessionHub() *consoleSessionHub {
	return &consoleSessionHub{sessions: map[string]*consoleSessionState{}}
}

func (h *consoleSessionHub) Create(topology, node, agentID string, req controlplane.ConsoleRequest) controlplane.ConsoleSession {
	now := time.Now().UTC()
	sess := controlplane.ConsoleSession{
		ID:        uuid.New().String(),
		ProjectID: "default",
		Workspace: topology,
		Topology:  topology,
		Node:      node,
		AgentID:   agentID,
		Status:    "queued",
		CreatedAt: now,
	}
	h.mu.Lock()
	h.sessions[sess.ID] = &consoleSessionState{
		session: sess,
		request: req,
		browser: make(chan *wsPeer, 1),
		agent:   make(chan *wsPeer, 1),
	}
	h.mu.Unlock()
	return sess
}

func (h *consoleSessionHub) Get(id string) (*consoleSessionState, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.sessions[id]
	if st == nil {
		return nil, fmt.Errorf("session not found")
	}
	return st, nil
}

func (h *consoleSessionHub) Snapshot(id string) (controlplane.ConsoleSession, error) {
	st, err := h.Get(id)
	if err != nil {
		return controlplane.ConsoleSession{}, err
	}
	return st.session, nil
}

func (h *consoleSessionHub) Update(id string, fn func(*controlplane.ConsoleSession)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.sessions[id]; st != nil {
		fn(&st.session)
	}
}
