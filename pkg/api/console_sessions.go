package api

import (
	"context"
	"errors"
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

var errConsoleSessionNotFound = errors.New("session not found")

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
				if sess.Status == controlplane.ConsoleSessionStatusQueued || sess.Status == controlplane.ConsoleSessionStatusRunning || sess.Status == controlplane.ConsoleSessionStatusCancelling {
					sess.Status = controlplane.ConsoleSessionStatusLost
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

func (h *consoleSessionHub) Create(ctx context.Context, topology, node, agentID string, req controlplane.ConsoleRequest) (controlplane.ConsoleSession, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.ConsoleSession{}, err
	}
	if h.store == nil {
		return controlplane.ConsoleSession{}, errors.New("console persistence unavailable")
	}
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
		Status:      controlplane.ConsoleSessionStatusQueued,
		RequestedBy: actor,
		Roles:       append([]string{}, req.Roles...),
		Policy:      req.Policy,
		TTY:         req.TTY == nil || *req.TTY,
		Audit: []controlplane.Event{{
			ProjectID: "default",
			Workspace: topology,
			Resource:  "sysbox_node." + node,
			Action:    "create",
			Status:    controlplane.ConsoleSessionStatusQueued,
			Actor:     actor,
			Roles:     append([]string{}, req.Roles...),
			Message:   "console session created",
			CreatedAt: now,
		}},
		CreatedAt: now,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return controlplane.ConsoleSession{}, err
	}
	if err := h.store.SaveConsoleSession(ctx, sess); err != nil {
		return controlplane.ConsoleSession{}, err
	}
	h.sessions[sess.ID] = newConsoleSessionState(sess, req)
	return sess, nil
}

func (h *consoleSessionHub) Get(id string) (*consoleSessionState, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.sessions[id]
	if st == nil {
		return nil, errConsoleSessionNotFound
	}
	return st, nil
}

func (h *consoleSessionHub) Snapshot(id string) (controlplane.ConsoleSession, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.sessions[id]
	if st == nil {
		return controlplane.ConsoleSession{}, errConsoleSessionNotFound
	}
	return cloneConsoleSession(st.session), nil
}

func cloneConsoleSession(sess controlplane.ConsoleSession) controlplane.ConsoleSession {
	sess.Roles = append([]string(nil), sess.Roles...)
	sess.Audit = append([]controlplane.Event(nil), sess.Audit...)
	for i := range sess.Audit {
		sess.Audit[i].Roles = append([]string(nil), sess.Audit[i].Roles...)
	}
	if sess.ExitCode != nil {
		code := *sess.ExitCode
		sess.ExitCode = &code
	}
	return sess
}

func (h *consoleSessionHub) Update(id string, fn func(*controlplane.ConsoleSession)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.sessions[id]
	if st == nil {
		return errConsoleSessionNotFound
	}
	if h.store == nil {
		return errors.New("console persistence unavailable")
	}
	sess := cloneConsoleSession(st.session)
	fn(&sess)
	if err := h.store.SaveConsoleSession(context.Background(), sess); err != nil {
		return err
	}
	st.session = cloneConsoleSession(sess)
	return nil
}

func (h *consoleSessionHub) Cancel(id, reason, actor string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.sessions[id]
	if st == nil {
		return errConsoleSessionNotFound
	}
	sess := st.session
	if sess.Status != controlplane.ConsoleSessionStatusClosed && sess.Status != controlplane.ConsoleSessionStatusFailed && sess.Status != controlplane.ConsoleSessionStatusCancelled && sess.Status != controlplane.ConsoleSessionStatusDenied && sess.Status != controlplane.ConsoleSessionStatusCancelling {
		sess.Status = controlplane.ConsoleSessionStatusCancelling
		sess.Err = reason
		sess.EndedAt = time.Time{}
		sess.Audit = append(append([]controlplane.Event(nil), sess.Audit...), controlplane.Event{
			ProjectID: st.session.ProjectID,
			Workspace: st.session.Workspace,
			Resource:  "sysbox_node." + st.session.Node,
			Action:    "cancel",
			Status:    controlplane.ConsoleSessionStatusCancelling,
			Actor:     actor,
			Roles:     append([]string{}, st.session.Roles...),
			Message:   reason + " by " + actor,
			CreatedAt: time.Now().UTC(),
		})
	}
	if h.store != nil {
		if err := h.store.SaveConsoleSession(context.Background(), sess); err != nil {
			return err
		}
	}
	st.session = sess
	select {
	case <-st.cancel:
	default:
		close(st.cancel)
	}
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
