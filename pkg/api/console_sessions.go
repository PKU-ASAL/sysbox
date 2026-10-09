package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

type consoleSessionHub struct {
	mu       sync.RWMutex
	sessions map[string]*consoleSessionState
	store    apiStore
	initErr  error
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
		items, err := store.ListConsoleSessions(context.Background(), "")
		if err != nil {
			h.initErr = err
			return h
		}
		loaded := map[string]*consoleSessionState{}
		for _, sess := range items {
			sess, err = recoverConsoleObservation(store, sess)
			if err != nil {
				h.initErr = err
				return h
			}
			loaded[sess.ID] = newConsoleSessionState(sess, controlplane.ConsoleRequest{})
		}
		h.sessions = loaded
	}
	return h
}

func recoverConsoleObservation(store consoleStore, sess controlplane.ConsoleSession) (controlplane.ConsoleSession, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if sess.Status != controlplane.ConsoleSessionStatusQueued && sess.Status != controlplane.ConsoleSessionStatusRunning {
			return sess, nil
		}
		next := cloneConsoleSession(sess)
		next.Status = controlplane.ConsoleSessionStatusLost
		next.Err = "api restarted before console session completion"
		next.EndedAt = time.Now().UTC()
		err := store.CompareAndSwapConsoleSession(context.Background(), sess, next)
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, persistence.ErrConflict) {
			return controlplane.ConsoleSession{}, err
		}
		current, readErr := store.GetConsoleSession(context.Background(), sess.ID)
		if readErr != nil {
			return controlplane.ConsoleSession{}, errors.Join(err, readErr)
		}
		if current == nil || persistence.ValidateConsoleSessionCAS(sess, *current) != nil {
			return controlplane.ConsoleSession{}, err
		}
		sess = cloneConsoleSession(*current)
		if sess.Status != controlplane.ConsoleSessionStatusQueued && sess.Status != controlplane.ConsoleSessionStatusRunning {
			return sess, nil
		}
	}
	return controlplane.ConsoleSession{}, persistence.ErrConflict
}

func newConsoleSession(topology, node, agentID string, req controlplane.ConsoleRequest) controlplane.ConsoleSession {
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
	return sess
}

func (h *consoleSessionHub) persistSession(ctx context.Context, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest, dispatch *controlplane.AgentCommand) (controlplane.ConsoleSession, error) {
	if h.initErr != nil {
		return controlplane.ConsoleSession{}, h.initErr
	}
	if h.store == nil {
		return controlplane.ConsoleSession{}, errors.New("console persistence unavailable")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return controlplane.ConsoleSession{}, err
	}
	var err error
	if dispatch != nil {
		err = h.store.CreateConsoleDispatch(ctx, sess, *dispatch)
	} else {
		err = h.store.SaveConsoleSession(ctx, sess)
	}
	if err != nil {
		return controlplane.ConsoleSession{}, err
	}
	h.sessions[sess.ID] = newConsoleSessionState(sess, req)
	return sess, nil
}

func (h *consoleSessionHub) Get(id string) (*consoleSessionState, error) {
	if h.initErr != nil {
		return nil, h.initErr
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.sessions[id]
	if st == nil {
		return nil, errConsoleSessionNotFound
	}
	return st, nil
}

func (h *consoleSessionHub) Snapshot(id string) (controlplane.ConsoleSession, error) {
	if h.initErr != nil {
		return controlplane.ConsoleSession{}, h.initErr
	}
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
	if h.initErr != nil {
		return h.initErr
	}
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
	if err := h.store.CompareAndSwapConsoleSession(context.Background(), st.session, sess); err != nil {
		return h.refreshAfterConflict(st, err)
	}
	st.session = cloneConsoleSession(sess)
	return nil
}

func (h *consoleSessionHub) Cancel(ctx context.Context, id, reason, actor string) error {
	if h.initErr != nil {
		return h.initErr
	}
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
	if h.store == nil {
		return errors.New("console persistence unavailable")
	}
	var err error
	if sess.Status == controlplane.ConsoleSessionStatusCancelling {
		err = h.store.CancelConsoleSession(ctx, st.session, sess)
	} else {
		err = h.store.CompareAndSwapConsoleSession(ctx, st.session, sess)
	}
	if err != nil {
		return h.refreshAfterConflict(st, err)
	}
	st.session = sess
	select {
	case <-st.cancel:
	default:
		close(st.cancel)
	}
	return nil
}

// Called with h.mu held. Refresh an obsolete observation so a later operation
// can make progress, but never replay a callback against a different state.
func (h *consoleSessionHub) refreshAfterConflict(st *consoleSessionState, err error) error {
	if !errors.Is(err, persistence.ErrConflict) {
		return err
	}
	current, readErr := h.store.GetConsoleSession(context.Background(), st.session.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if current == nil || persistence.ValidateConsoleSessionCAS(st.session, *current) != nil {
		return err
	}
	st.session = cloneConsoleSession(*current)
	// Refresh is not a successful cancellation transaction. Only Cancel may
	// signal the local relay after the durable session/outbox pair is accepted.
	return err
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
