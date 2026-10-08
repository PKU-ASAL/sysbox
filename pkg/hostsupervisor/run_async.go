package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/transport"
)

// RunObservation never interprets cancellation or an abandoned durable intent
// as proof that provider effects have stopped.
type RunObservation struct {
	Status   string          `json:"status"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

type runRegistry struct {
	mu         sync.Mutex
	active     map[string]*activeRun
	admissions map[string]*runAdmission
}
type runAdmission struct {
	mu    sync.Mutex
	users int
}
type activeRun struct {
	mu             sync.Mutex
	grant          controlplane.SignedExecutionGrant
	deadline       time.Time
	safetyDeadline atomic.Pointer[time.Time]
	ctx            context.Context
	cancel         context.CancelFunc
	changed        chan struct{}
	done           chan struct{}
}

var runRegistryInit sync.Mutex

func (c *RunCoordinator) registry() *runRegistry {
	runRegistryInit.Lock()
	defer runRegistryInit.Unlock()
	if c.async == nil {
		c.async = &runRegistry{active: make(map[string]*activeRun), admissions: make(map[string]*runAdmission)}
	}
	return c.async
}

func (r *runRegistry) admit(key string) func() {
	r.mu.Lock()
	a := r.admissions[key]
	if a == nil {
		a = &runAdmission{}
		r.admissions[key] = a
	}
	a.users++
	r.mu.Unlock()
	a.mu.Lock()
	return func() {
		a.mu.Unlock()
		r.mu.Lock()
		a.users--
		if a.users == 0 {
			delete(r.admissions, key)
		}
		r.mu.Unlock()
	}
}

func runIdentity(g controlplane.SignedExecutionGrant) string {
	grant := g.Grant
	grant.ExpiresAt = time.Time{}
	raw, _ := json.Marshal(struct {
		Grant controlplane.ExecutionGrant
		Boot  string
	}{grant, g.BootID})
	return string(raw)
}

func (c *RunCoordinator) authorizationTimeout() time.Duration {
	if c.AuthorizationTimeout > 0 {
		return c.AuthorizationTimeout
	}
	return 2 * time.Second
}

// A failed or unresponsive online check closes authority. The buffered reply
// prevents a delayed cooperative checker from blocking after its deadline.
func (c *RunCoordinator) authorizeBounded(ctx context.Context, grant controlplane.SignedExecutionGrant) error {
	if c.Authorize == nil {
		return effectgate.ErrFenced
	}
	bounded, cancel := context.WithTimeout(ctx, c.authorizationTimeout())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- c.Authorize(bounded, grant) }()
	select {
	case err := <-result:
		if bounded.Err() != nil {
			return bounded.Err()
		}
		return err
	case <-bounded.Done():
		return bounded.Err()
	}
}

func (c *RunCoordinator) asyncGate(g controlplane.SignedExecutionGrant) (*effectgate.Gate, error) {
	gate, err := c.gate(g)
	if err != nil {
		return nil, err
	}
	gate.Authorize = func(ctx context.Context, _ controlplane.ExecutionGrant) error { return c.authorizeBounded(ctx, g) }
	return gate, nil
}

func (c *RunCoordinator) verifyHistorical(req RunRequest, g controlplane.SignedExecutionGrant) error {
	if err := ValidateRunGrant(g, req, g.BootID); err != nil {
		return err
	}
	if g.Grant.AgentID != c.AgentID {
		return effectgate.ErrFenced
	}
	return controlplane.VerifyExecutionGrant(c.IssuerKey, g, g.BootID, g.IssuedAt)
}

// StartAsync durably reserves the identity before launching exactly one
// callback. The callback belongs to the supervisor service, not the RPC.
func (c *RunCoordinator) StartAsync(requestCtx, serviceCtx context.Context, req RunRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (RunObservation, error) {
	if err := c.verifyHistorical(req, g); err != nil {
		return RunObservation{}, err
	}
	if effect == nil || serviceCtx == nil || serviceCtx.Err() != nil {
		return RunObservation{}, effectgate.ErrFenced
	}
	gate, err := c.asyncGate(g)
	if err != nil {
		return RunObservation{}, err
	}
	registry := c.registry()
	releaseAdmission := registry.admit(runIdentity(g))
	defer releaseAdmission()
	// Historical retries may carry an older envelope after a durable renewal.
	prior, readErr := gate.ReadRun(requestCtx, g, req.ActionID)
	if readErr == nil {
		return c.observeReservation(requestCtx, req, prior)
	}
	if !errors.Is(readErr, effectgate.ErrNotFound) {
		return RunObservation{}, readErr
	}
	now := time.Now()
	if err := ValidateRunGrant(g, req, c.HostBootID); err != nil {
		return RunObservation{}, err
	}
	if err := controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, now); err != nil {
		return RunObservation{}, err
	}
	budget, err := controlplane.ExecutionGrantBudget(g, now)
	if err != nil {
		return RunObservation{}, err
	}
	deadline := now.Add(budget)
	admissionCtx, cancelAdmission := context.WithDeadline(requestCtx, deadline)
	reservation, err := gate.ReserveRun(admissionCtx, g, req.ActionID)
	cancelAdmission()
	if err != nil {
		return RunObservation{}, err
	}
	if !reservation.Admitted {
		return c.observeReservation(requestCtx, req, reservation)
	}
	effectCtx, cancel := context.WithCancel(serviceCtx)
	active := &activeRun{grant: g, deadline: deadline, ctx: effectCtx, cancel: cancel, changed: make(chan struct{}, 1), done: make(chan struct{})}
	active.safetyDeadline.Store(&deadline)
	registry.mu.Lock()
	registry.active[runIdentity(g)] = active
	registry.mu.Unlock()
	if effectCtx.Err() != nil || !time.Now().Before(deadline) {
		cancel()
		registry.mu.Lock()
		delete(registry.active, runIdentity(g))
		registry.mu.Unlock()
		close(active.done)
		return RunObservation{Status: "uncertain"}, nil
	}
	go c.watchRun(active)
	go watchRunDeadline(active)
	go c.executeRun(req, active, effect)
	return RunObservation{Status: "running"}, nil
}

func (c *RunCoordinator) findActive(g controlplane.SignedExecutionGrant) *activeRun {
	registry := c.registry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.active[runIdentity(g)]
}

func (c *RunCoordinator) watchRun(active *activeRun) {
	interval := c.AuthorizationInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		active.mu.Lock()
		deadline := active.deadline
		grant := active.grant
		active.mu.Unlock()
		select {
		case <-active.ctx.Done():
			return
		case <-active.done:
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithDeadline(active.ctx, deadline)
			err := c.authorizeBounded(checkCtx, grant)
			cancel()
			if err != nil {
				active.mu.Lock()
				// A response about a replaced envelope cannot revoke its successor.
				if active.grant.Grant.ExpiresAt.Equal(grant.Grant.ExpiresAt) || !time.Now().Before(active.deadline) {
					active.cancel()
				}
				active.mu.Unlock()
			}
		}
	}
}

// Deadline cancellation cannot wait for journal I/O or online authorization
// holding active.mu. The immutable time retains its monotonic clock reading.
func watchRunDeadline(active *activeRun) {
	for {
		deadline := active.safetyDeadline.Load()
		timer := time.NewTimer(time.Until(*deadline))
		select {
		case <-active.ctx.Done():
			timer.Stop()
			return
		case <-active.done:
			timer.Stop()
			return
		case <-active.changed:
			timer.Stop()
		case <-timer.C:
			if !time.Now().Before(*active.safetyDeadline.Load()) {
				active.cancel()
				return
			}
		}
	}
}

func (c *RunCoordinator) executeRun(req RunRequest, active *activeRun, effect func(context.Context) (json.RawMessage, error)) {
	defer close(active.done)
	defer func() {
		active.mu.Lock()
		key := runIdentity(active.grant)
		active.mu.Unlock()
		registry := c.registry()
		registry.mu.Lock()
		delete(registry.active, key)
		registry.mu.Unlock()
	}()
	if active.ctx.Err() != nil {
		return
	}
	effectCtx := transport.WithParentExecutionGrantResolver(active.ctx, func() (controlplane.SignedExecutionGrant, bool) {
		active.mu.Lock()
		defer active.mu.Unlock()
		if active.ctx.Err() != nil || !time.Now().Before(active.deadline) {
			return controlplane.SignedExecutionGrant{}, false
		}
		return active.grant, true
	})
	raw, err := effect(effectCtx)
	if err != nil && c.OnExecutionError != nil {
		c.OnExecutionError(req, err)
	}
	var resultObject map[string]json.RawMessage
	if err == nil && (json.Unmarshal(raw, &resultObject) != nil || resultObject == nil) {
		err = effectgate.ErrUncertain
	}
	active.mu.Lock()
	if err != nil || active.ctx.Err() != nil || !time.Now().Before(active.deadline) {
		active.cancel()
		active.mu.Unlock()
		return
	}
	current := active.grant
	gate, err := c.asyncGate(current)
	if err == nil {
		commitCtx, cancel := context.WithDeadline(active.ctx, active.deadline)
		err = gate.CommitRun(commitCtx, current, req.ActionID, raw)
		cancel()
	}
	active.cancel()
	active.mu.Unlock()
	if err != nil {
		return
	}
	// Persisted success survives service cancellation. Recovery and observation
	// retry sealing/reporting if shutdown interrupts this attempt.
	reportCtx, cancel := context.WithTimeout(context.Background(), c.authorizationTimeout())
	defer cancel()
	_, _ = c.completedEvidence(reportCtx, req, current, raw)
}

func (c *RunCoordinator) ObserveAsync(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant) (RunObservation, error) {
	if err := c.verifyHistorical(req, g); err != nil {
		return RunObservation{}, err
	}
	gate, err := c.asyncGate(g)
	if err != nil {
		return RunObservation{}, err
	}
	reservation, err := gate.ReadRun(ctx, g, req.ActionID)
	if err != nil {
		return RunObservation{}, err
	}
	return c.observeReservation(ctx, req, reservation)
}

func (c *RunCoordinator) observeReservation(ctx context.Context, req RunRequest, reservation effectgate.RunReservation) (RunObservation, error) {
	if reservation.State == effectgate.RunComplete {
		evidence, err := c.completedEvidence(ctx, req, reservation.Grant, reservation.Result)
		return RunObservation{Status: "completed", Evidence: evidence}, err
	}
	status := "uncertain"
	if active := c.findActive(reservation.Grant); active != nil {
		active.mu.Lock()
		if active.ctx.Err() == nil && time.Now().Before(active.deadline) {
			status = "running"
		}
		active.mu.Unlock()
	}
	return RunObservation{Status: status}, nil
}

func (c *RunCoordinator) completedEvidence(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant, raw json.RawMessage) (json.RawMessage, error) {
	if len(c.HostKey) != ed25519.PrivateKeySize {
		return raw, nil
	}
	gate, err := c.asyncGate(g)
	if err != nil {
		return nil, err
	}
	_, err = gate.SealStop(ctx, g.Grant, req.ActionID, func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignRunStopReceipt(c.HostKey, grant, c.HostBootID, rand.Text(), raw, time.Now().UTC())
	})
	if err != nil {
		return nil, err
	}
	if c.ReportRunID != nil || c.Report != nil {
		err = gate.ReportStop(ctx, g.Grant, req.ActionID, func(ctx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
			if c.ReportRunID != nil {
				return c.ReportRunID(ctx, req.RunID, receipt, raw)
			}
			return c.Report(ctx, receipt, raw)
		})
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}

// Renew persists the replacement envelope before advancing the monotonic
// deadline. A canceled, expired, completed or cold intent cannot be revived.
func (c *RunCoordinator) Renew(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant) error {
	if err := ValidateRunGrant(g, req, c.HostBootID); err != nil {
		return err
	}
	if err := controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return err
	}
	active := c.findActive(g)
	if active == nil {
		return effectgate.ErrUncertain
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	now := time.Now()
	if active.ctx.Err() != nil || !now.Before(active.deadline) {
		active.cancel()
		return effectgate.ErrFenced
	}
	budget, err := controlplane.ExecutionGrantBudget(g, now)
	if err != nil {
		return err
	}
	gate, err := c.asyncGate(g)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithDeadline(ctx, active.deadline)
	defer cancel()
	if err := gate.RenewRun(bounded, active.grant, g, req.ActionID); err != nil {
		return err
	}
	if active.ctx.Err() != nil || !time.Now().Before(active.deadline) {
		active.cancel()
		return effectgate.ErrFenced
	}
	active.grant = g
	active.deadline = now.Add(budget)
	deadline := active.deadline
	active.safetyDeadline.Store(&deadline)
	select {
	case active.changed <- struct{}{}:
	default:
	}
	return nil
}

func (c *RunCoordinator) Cancel(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant) error {
	if err := c.verifyHistorical(req, g); err != nil {
		return err
	}
	if active := c.findActive(g); active != nil {
		active.cancel()
		return nil
	}
	gate, err := c.asyncGate(g)
	if err != nil {
		return err
	}
	if _, err := gate.ReadRun(ctx, g, req.ActionID); err != nil {
		return err
	}
	return nil
}
