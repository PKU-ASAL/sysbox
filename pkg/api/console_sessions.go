package api

import (
	"context"
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
	store    apiStore
}

type consoleSessionState struct {
	session controlplane.ConsoleSession
	request controlplane.ConsoleRequest
	browser chan *wsPeer
	agent   chan *wsPeer
	cancel  chan struct{}
	done    chan struct{}
}

type wsPeer struct {
	conn *websocket.Conn
}

func newConsoleSessionHub(store apiStore) *consoleSessionHub {
	h := &consoleSessionHub{sessions: map[string]*consoleSessionState{}, store: store}
	if store != nil {
		if items, err := store.ListConsoleSessions(context.Background(), ""); err == nil {
			for _, sess := range items {
				if sess.Status == "queued" || sess.Status == "running" {
					sess.Status = "lost"
					sess.Err = "api restarted before console session completion"
					sess.EndedAt = time.Now().UTC()
					_ = store.SaveConsoleSession(context.Background(), sess)
				}
				h.sessions[sess.ID] = newConsoleSessionState(sess, controlplane.ConsoleRequest{})
			}
		}
	}
	return h
}

func (h *consoleSessionHub) Create(topology, node, agentID string, req controlplane.ConsoleRequest) controlplane.ConsoleSession {
	now := time.Now().UTC()
	actor := req.RequestedBy
	if actor == "" {
		actor = "api"
	}
	sess := controlplane.ConsoleSession{
		ID:          uuid.New().String(),
		ProjectID:   "default",
		Workspace:   topology,
		Topology:    topology,
		Node:        node,
		AgentID:     agentID,
		Status:      "queued",
		RequestedBy: actor,
		Roles:       append([]string{}, req.Roles...),
		Policy:      req.Policy,
		TTY:         req.TTY == nil || *req.TTY,
		Audit: []controlplane.Event{{
			ProjectID: "default",
			Workspace: topology,
			Resource:  "sysbox_node." + node,
			Action:    "create",
			Status:    "queued",
			Actor:     actor,
			Roles:     append([]string{}, req.Roles...),
			Message:   "console session created",
			CreatedAt: now,
		}},
		CreatedAt: now,
	}
	h.mu.Lock()
	h.sessions[sess.ID] = newConsoleSessionState(sess, req)
	h.mu.Unlock()
	h.persist(sess)
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
	var sess controlplane.ConsoleSession
	h.mu.Lock()
	if st := h.sessions[id]; st != nil {
		fn(&st.session)
		sess = st.session
	}
	h.mu.Unlock()
	if sess.ID != "" {
		h.persist(sess)
	}
}

func (h *consoleSessionHub) Cancel(id, reason, actor string) error {
	h.mu.Lock()
	st := h.sessions[id]
	if st == nil {
		h.mu.Unlock()
		return fmt.Errorf("session not found")
	}
	if st.session.Status != "closed" && st.session.Status != "failed" && st.session.Status != "cancelled" {
		st.session.Status = "cancelled"
		st.session.Err = reason
		st.session.EndedAt = time.Now().UTC()
		st.session.Audit = append(st.session.Audit, controlplane.Event{
			ProjectID: st.session.ProjectID,
			Workspace: st.session.Workspace,
			Resource:  "sysbox_node." + st.session.Node,
			Action:    "cancel",
			Status:    "cancelled",
			Actor:     actor,
			Roles:     append([]string{}, st.session.Roles...),
			Message:   reason + " by " + actor,
			CreatedAt: time.Now().UTC(),
		})
	}
	select {
	case <-st.cancel:
	default:
		close(st.cancel)
	}
	sess := st.session
	h.mu.Unlock()
	h.persist(sess)
	return nil
}

func newConsoleSessionState(sess controlplane.ConsoleSession, req controlplane.ConsoleRequest) *consoleSessionState {
	return &consoleSessionState{
		session: sess,
		request: req,
		browser: make(chan *wsPeer, 1),
		agent:   make(chan *wsPeer, 1),
		cancel:  make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (h *consoleSessionHub) persist(sess controlplane.ConsoleSession) {
	if h.store != nil {
		_ = h.store.SaveConsoleSession(context.Background(), sess)
	}
}
