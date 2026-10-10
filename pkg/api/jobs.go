package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

const (
	DefaultAgentID = "local"
)

// Jobs is a run store backed by in-memory map + the API persistence store.
// Per-topology mutexes prevent concurrent apply/destroy on the same topology.
type Jobs struct {
	mu                 sync.RWMutex
	runs               map[string]*controlplane.Run
	runsDir            string // root directory for runs, e.g. "runs"
	store              apiStore
	recoverInterrupted bool
	runLeaseTTL        time.Duration
	expiredPolicy      string
	logs               *RunLogHub
	topologyLocks      *TopologyLocks
}

type runStartOptions struct {
	ParentID     string
	Revision     string
	Target       string
	AgentID      string
	UnsafeState  bool
	OperationKey string
	Inputs       map[string]string
	DeadlineAt   time.Time
	SnapshotPath string
}

func newJobs(runsDir string, store apiStore) *Jobs {
	return newJobsWithRecovery(runsDir, store, true)
}

func newJobsWithRecovery(runsDir string, store apiStore, recoverInterrupted bool) *Jobs {
	return newJobsWithPolicy(runsDir, store, recoverInterrupted, 30*time.Minute, "fail_recoverable")
}

func newJobsWithPolicy(runsDir string, store apiStore, recoverInterrupted bool, runLeaseTTL time.Duration, expiredPolicy string) *Jobs {
	if store == nil {
		store = &localAPIStore{runsDir: runsDir}
	}
	if runLeaseTTL <= 0 {
		runLeaseTTL = 30 * time.Minute
	}
	if expiredPolicy == "" {
		expiredPolicy = "fail_recoverable"
	}
	j := &Jobs{
		runs:               make(map[string]*controlplane.Run),
		runsDir:            runsDir,
		store:              store,
		recoverInterrupted: recoverInterrupted,
		runLeaseTTL:        runLeaseTTL,
		expiredPolicy:      expiredPolicy,
		logs:               newRunLogHub(),
		topologyLocks:      newTopologyLocks(),
	}
	j.load()
	return j
}

// lockTopology acquires a per-topology mutex. Returns a function to unlock.
// This ensures that concurrent apply/destroy requests for the same topology
// are serialised, preventing state file corruption and double-create bugs.
func (j *Jobs) lockTopology(topology string) func() {
	return j.topologyLocks.Lock(topology)
}

// load populates the in-memory store from persisted run records.
func (j *Jobs) load() {
	runs, err := j.store.LoadRuns(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[api] load runs: %v\n", err)
		return
	}
	if j.recoverInterrupted {
		runs = markInterruptedRuns(runs)
	}
	for _, r := range runs {
		run := r
		normalizeRunProductFields(&run)
		j.logs.Ensure(run.ID, run.Status.IsTerminal())
		j.runs[run.ID] = &run
	}
	j.loadCheckpoints()
}

func (j *Jobs) loadCheckpoints() {
	pattern := filepath.Join(j.runsDir, "*", "runs", "*.checkpoint.json")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		id := filepath.Base(f)
		id = id[:len(id)-len(".checkpoint.json")]
		if _, ok := j.runs[id]; ok {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var cp struct {
			RunID     string    `json:"run_id"`
			Topology  string    `json:"topology"`
			Operation string    `json:"operation"`
			Status    string    `json:"status"`
			StartedAt time.Time `json:"started_at"`
			EndedAt   time.Time `json:"ended_at"`
		}
		if err := json.Unmarshal(data, &cp); err != nil || cp.RunID == "" {
			continue
		}
		status := controlplane.RunFailed
		errMsg := "server restarted before run completion"
		if cp.Status == "done" {
			status = controlplane.RunDone
			errMsg = ""
		}
		r := &controlplane.Run{
			ID:          cp.RunID,
			ProjectID:   "default",
			Workspace:   cp.Topology,
			Topology:    cp.Topology,
			Op:          cp.Operation,
			Status:      status,
			Err:         errMsg,
			Recoverable: status == controlplane.RunFailed,
			StartedAt:   cp.StartedAt,
			EndedAt:     cp.EndedAt,
		}
		j.logs.Ensure(r.ID, true)
		j.runs[r.ID] = r
	}
}

// persist writes a run record through the configured API store.
func (j *Jobs) persist(r *controlplane.Run) error {
	rec := runRecord(*r)
	// Sensitive apply inputs are transient: the agent binds var.<name> from the
	// run it receives in-memory (claim/dispatch), but they must never survive in
	// the durable run record. Strip them here so a canary/flag plaintext never
	// enters the store.
	rec.Inputs = nil
	if err := j.store.SaveRun(context.Background(), rec); err != nil {
		fmt.Fprintf(os.Stderr, "[api] persist run: %v\n", err)
		current, readErr := j.store.GetRun(context.Background(), rec.ID)
		j.mu.Lock()
		if readErr == nil && current != nil {
			current.Inputs = inputsForRun(j.runs[rec.ID], current)
			j.runs[rec.ID] = current
		} else {
			delete(j.runs, rec.ID)
		}
		j.mu.Unlock()
		return err
	}
	return nil
}

func runRecord(r controlplane.Run) controlplane.Run {
	normalizeRunProductFields(&r)
	return r
}

func normalizeRunProductFields(r *controlplane.Run) {
	if r == nil {
		return
	}
	persistence.NormalizeRun(r)
}

// cloneInputs returns a shallow copy so re-attached transient inputs never
// alias the in-memory run's map.
func cloneInputs(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func inputsForRun(cached, current *controlplane.Run) map[string]string {
	if cached == nil || current == nil || current.Status.IsTerminal() || !persistence.SameRunIncarnation(*cached, *current) {
		return nil
	}
	return cloneInputs(cached.Inputs)
}

func (j *Jobs) start(topology, op string) *controlplane.Run {
	return j.startWithOptions(topology, op, runStartOptions{})
}

func (j *Jobs) startWithOptions(topology, op string, opts runStartOptions) *controlplane.Run {
	run, _, _ := j.startWithResult(topology, op, opts)
	return run
}

func (j *Jobs) startWithResult(topology, op string, opts runStartOptions) (*controlplane.Run, bool, error) {
	r := newRun(topology, op, opts)
	j.mu.Lock()
	if opts.OperationKey != "" {
		stored, err := j.store.GetRun(context.Background(), r.ID)
		if err == nil && stored != nil {
			if stored.Topology != r.Topology || stored.OperationKey != r.OperationKey || stored.Op != r.Op {
				j.mu.Unlock()
				return nil, false, persistence.ErrConflict
			}
			previous := j.runs[r.ID]
			stored.Inputs = inputsForRun(previous, stored)
			if stored.Status.IsActive() && stored.Op == "apply" && len(stored.OperationKey) == sha256.Size*2 && stored.OperationKey == applyOperationKey(stored.Revision, opts.Inputs, stored.UnsafeState) {
				stored.Inputs = cloneInputs(opts.Inputs)
			}
			newRound := previous == nil || !previous.QueuedAt.Equal(stored.QueuedAt)
			j.runs[r.ID] = stored
			j.mu.Unlock()
			if newRound && !stored.Status.IsTerminal() {
				j.logs.Reopen(r.ID)
			} else {
				j.logs.Ensure(r.ID, stored.Status.IsTerminal())
			}
			return stored, false, nil
		}
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			j.mu.Unlock()
			return nil, false, err
		}
	}
	if err := j.store.SaveRun(context.Background(), persistence.DurableRun(*r)); err != nil {
		if opts.OperationKey != "" {
			if stored, readErr := j.store.GetRun(context.Background(), r.ID); readErr == nil && stored != nil && stored.Topology == r.Topology && stored.OperationKey == r.OperationKey {
				stored.Inputs = cloneInputs(opts.Inputs)
				j.runs[r.ID] = stored
				j.mu.Unlock()
				return stored, false, nil
			}
		}
		j.mu.Unlock()
		return nil, false, err
	}
	j.runs[r.ID] = r
	j.mu.Unlock()
	j.logs.Ensure(r.ID, false)
	return r, true, nil
}

// forceStart overwrites any existing record under r's id and persists it. Unlike
// startWithResult it never dedups: it is used to replace a terminal run with a
// fresh attempt that reuses the same deterministic id, so later applies coalesce
// onto the in-flight run instead of the stale terminal record.
func (j *Jobs) forceStart(r *controlplane.Run) error {
	if r == nil {
		return persistence.ErrInvalidRecord
	}
	normalizeRunProductFields(r)
	stored, err := j.store.RestartRun(context.Background(), *r)
	if err != nil {
		return err
	}
	stored.Inputs = cloneInputs(r.Inputs)
	*r = *stored
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	j.logs.Reopen(r.ID)
	return nil
}

func newRun(topology, op string, opts runStartOptions) *controlplane.Run {
	now := time.Now()
	runID := uuid.New().String()
	if opts.OperationKey != "" {
		digest := sha256.Sum256([]byte(topology + "\x00" + op + "\x00" + opts.OperationKey))
		runID = fmt.Sprintf("op-%x", digest[:])
	}
	r := &controlplane.Run{
		ID:           runID,
		ProjectID:    "default",
		Workspace:    topology,
		Topology:     topology,
		Op:           op,
		Status:       controlplane.RunQueued,
		ParentID:     opts.ParentID,
		Revision:     opts.Revision,
		Target:       opts.Target,
		OperationKey: opts.OperationKey,
		AgentID:      opts.AgentID,
		UnsafeState:  opts.UnsafeState,
		SnapshotPath: opts.SnapshotPath,
		Protocol:     controlplane.AgentProtocolVersion,
		Inputs:       opts.Inputs,
		DeadlineAt:   opts.DeadlineAt,
		LeaseOwner:   "sysbox-api",
		QueuedAt:     now,
		StartedAt:    now,
	}
	normalizeRunProductFields(r)
	r.LeaseOwner = fmt.Sprintf("sysbox-api:%s:%s", r.Op, r.ID)
	return r
}

func (j *Jobs) remember(r *controlplane.Run) {
	if r == nil {
		return
	}
	snapshot := runRecord(*r)
	j.mu.Lock()
	j.runs[r.ID] = &snapshot
	j.mu.Unlock()
	j.logs.Ensure(r.ID, false)
}

func (j *Jobs) assign(r *controlplane.Run, agentID string) error {
	j.mu.Lock()
	next := *r
	next.MarkAssigned(agentID, time.Now())
	j.mu.Unlock()
	if err := j.replace(&next); err != nil {
		return err
	}
	*r = next
	return nil
}

func (j *Jobs) markRunning(r *controlplane.Run) {
	j.mu.Lock()
	r.Status = controlplane.RunRunning
	if r.StartedAt.IsZero() || r.StartedAt.Equal(r.QueuedAt) {
		r.StartedAt = time.Now()
	}
	j.mu.Unlock()
	j.persist(r)
}

func (j *Jobs) claim(runID, agentID string) (*controlplane.Run, error) {
	return j.claimExpected(runID, agentID, nil)
}

func (j *Jobs) claimExpected(runID, agentID string, expected *controlplane.RunClaim) (*controlplane.Run, error) {
	intent, err := j.store.GetRun(context.Background(), runID)
	if err != nil {
		return nil, err
	}
	if expected != nil && (!expected.QueuedAt.Equal(intent.QueuedAt) || expected.Revision != intent.Revision) {
		return nil, persistence.ErrConflict
	}
	j.mu.RLock()
	inputs := inputsForRun(j.runs[runID], intent)
	j.mu.RUnlock()
	if keyedInputsRequired(intent, inputs) {
		return nil, errApplyInputsRequired
	}
	if intent.Op == "apply" && !strings.HasPrefix(intent.RequestFingerprint, "apply-request:") && len(intent.OperationKey) == sha256.Size*2 && intent.OperationKey != applyOperationKey(intent.Revision, inputs, intent.UnsafeState) {
		return nil, errApplyInputsRequired
	}
	claimCtx := persistence.WithRunClaimIdentity(context.Background(), *intent)
	owner := fmt.Sprintf("%s:%s:%d", agentID, runID, time.Now().UnixNano())
	ttl := j.runLeaseTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if claimed, ok, err := j.store.ClaimRun(claimCtx, runID, agentID, owner, ttl); err != nil {
		return nil, err
	} else if ok && claimed != nil {
		j.mu.Lock()
		// Inputs are transient and stripped from the durable run record; the
		// in-memory run still holds them for the duration of the run, so
		// re-attach them to the claim response. This keeps var.<name> binding
		// working without ever re-reading the plaintext from the store.
		claimed.Inputs = cloneInputs(inputs)
		j.runs[runID] = claimed
		j.mu.Unlock()
		j.logs.Ensure(runID, false)
		return claimed, nil
	} else if claimed != nil {
		return nil, fmt.Errorf("%w: run status %q cannot be claimed", persistence.ErrConflict, claimed.Status)
	}
	return nil, fmt.Errorf("%w: durable run claim was not granted", persistence.ErrConflict)
}

func (j *Jobs) renewLease(runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, error) {
	renewed, ok, err := j.store.RenewRunLease(context.Background(), runID, agentID, owner, ttl)
	if err != nil {
		return nil, err
	}
	if !ok || renewed == nil {
		return nil, fmt.Errorf("run lease cannot be renewed")
	}
	j.mu.Lock()
	// Preserve the transient inputs the in-memory run still holds: the durable
	// record is stripped, and the agent decodes this response over the same run
	// pointer it is executing, so dropping inputs here would null them mid-run.
	renewed.Inputs = inputsForRun(j.runs[runID], renewed)
	j.runs[runID] = renewed
	j.mu.Unlock()
	return renewed, nil
}

func runLeasable(run controlplane.Run, agentID string, now time.Time) bool {
	return run.CanBeClaimedBy(agentID, now)
}

func (j *Jobs) markExpiredLeases(now time.Time) {
	if j.expiredPolicy != "" && j.expiredPolicy != "fail_recoverable" {
		return
	}
	runs, err := j.store.LoadRuns(context.Background())
	if err != nil {
		return
	}
	for _, run := range latestRunsByID(runs) {
		if run.Status != controlplane.RunRunning || run.LeaseUntil.IsZero() || run.LeaseUntil.After(now) {
			continue
		}
		current, _, err := j.store.ExpireRunLease(context.Background(), run.ID, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[api] expire run lease: %v\n", err)
			continue
		}
		if current == nil {
			continue
		}
		// The durable transition already committed. Cache its result without a
		// second SaveRun that could overwrite a newer renewal or completion.
		j.mu.Lock()
		if old := j.runs[current.ID]; old != nil && !current.Status.IsTerminal() && old.LeaseOwner == current.LeaseOwner && old.Attempt == current.Attempt {
			current.Inputs = inputsForRun(old, current)
		}
		j.runs[current.ID] = current
		j.mu.Unlock()
		if current.Status.IsTerminal() {
			j.logs.Close(current.ID)
		}
	}
}

// markConvergenceDeadlineExceeded fails runs that have been converging — or
// waiting to converge — longer than timeout.
//
// The convergence bound belongs to sysbox, not to each consumer: without it an
// apply can hang indefinitely and nothing ever says so. A run that has been
// active past its deadline is declared failed here, which flows through the
// normal status projection (Applied → False → phase failed). timeout <= 0
// disables the bound. Destroy runs are not convergence and are left alone.
func (j *Jobs) markConvergenceDeadlineExceeded(now time.Time, timeout time.Duration) {
	if timeout <= 0 {
		return
	}

	j.mu.RLock()
	snapshots := make([]controlplane.Run, 0, len(j.runs))
	for _, r := range j.runs {
		snapshots = append(snapshots, *r)
	}
	j.mu.RUnlock()

	for i := range snapshots {
		r := &snapshots[i]
		if r.Op == "destroy" || !isConvergingStatus(r.Status) {
			continue
		}
		deadline := r.DeadlineAt
		if deadline.IsZero() {
			if r.StartedAt.IsZero() {
				continue
			}
			deadline = r.StartedAt.Add(timeout)
		}
		if !now.After(deadline) {
			continue
		}
		r.MarkFinished(fmt.Errorf("convergence deadline exceeded (deadline %s)", deadline), now)
		j.replace(r)
	}
}

func isConvergingStatus(status controlplane.RunStatus) bool {
	switch status {
	case controlplane.RunQueued, controlplane.RunAssigned, controlplane.RunRunning:
		return true
	default:
		return false
	}
}

func (j *Jobs) runnableForAgent(agentID string) []*controlplane.Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*controlplane.Run, 0)
	for _, r := range j.runs {
		if r.AgentID == agentID && r.Status == controlplane.RunAssigned {
			out = append(out, r)
		}
	}
	return out
}

func latestRunsByID(runs []controlplane.Run) []controlplane.Run {
	latest := map[string]controlplane.Run{}
	for _, run := range runs {
		prev, ok := latest[run.ID]
		if !ok || run.QueuedAt.After(prev.QueuedAt) || run.QueuedAt.Equal(prev.QueuedAt) && (runStatusRank(run.Status) >= runStatusRank(prev.Status) || runRecordTime(run).After(runRecordTime(prev))) {
			latest[run.ID] = run
		}
	}
	out := make([]controlplane.Run, 0, len(latest))
	for _, run := range latest {
		out = append(out, run)
	}
	return out
}

func runStatusRank(status controlplane.RunStatus) int {
	return status.Rank()
}

func runRecordTime(run controlplane.Run) time.Time {
	switch {
	case !run.EndedAt.IsZero():
		return run.EndedAt
	case !run.StartedAt.IsZero():
		return run.StartedAt
	case !run.AssignedAt.IsZero():
		return run.AssignedAt
	default:
		return run.QueuedAt
	}
}

func (j *Jobs) hasRunning(topology string) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	for _, r := range j.runs {
		if r.Topology == topology && r.Status.IsActive() {
			return true
		}
	}
	return false
}

func (j *Jobs) startChild(parent *controlplane.Run) (*controlplane.Run, error) {
	target := parent.Target
	if parent.Op == "reset" {
		target = ""
	}
	run, _, err := j.startWithResult(parent.Topology, parent.Op, runStartOptions{
		ParentID:     parent.ID,
		SnapshotPath: parent.SnapshotPath,
		Revision:     parent.Revision,
		Target:       target,
		AgentID:      parent.AgentID,
		UnsafeState:  parent.UnsafeState,
	})
	return run, err
}

func (j *Jobs) finish(r *controlplane.Run, err error) error {
	if r == nil {
		return persistence.ErrInvalidRecord
	}
	j.mu.Lock()
	next := *r
	next.MarkFinished(err, time.Now())
	// Sensitive inputs are only needed while the run is executing; the executor
	// has already bound var.<name> by the time a run goes terminal, so drop them
	// from the in-memory entry to avoid exposing them via get/list.
	next.Inputs = nil
	j.mu.Unlock()
	if err := j.replace(&next); err != nil {
		return err
	}
	*r = next
	return nil
}

func (j *Jobs) replace(r *controlplane.Run) error {
	if r == nil {
		return persistence.ErrInvalidRecord
	}
	normalizeRunProductFields(r)
	if r.Status.IsTerminal() {
		r.Inputs = nil
	}
	if err := j.persist(r); err != nil {
		return err
	}
	j.mu.Lock()
	j.runs[r.ID] = r
	j.mu.Unlock()
	if r.Status.IsTerminal() {
		j.logs.Close(r.ID)
	}
	return nil
}

func (j *Jobs) get(id string) (*controlplane.Run, bool) {
	if run, err := j.store.GetRun(context.Background(), id); err == nil && run != nil {
		j.mu.Lock()
		if existing, ok := j.runs[id]; ok {
			run.Inputs = inputsForRun(existing, run)
			if preferRun(existing, run) {
				j.mu.Unlock()
				return existing, true
			}
		} else {
			j.logs.Ensure(run.ID, run.Status.IsTerminal())
		}
		j.runs[id] = run
		j.mu.Unlock()
		return run, true
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	r, ok := j.runs[id]
	return r, ok
}

func preferRun(a, b *controlplane.Run) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	if !a.QueuedAt.Equal(b.QueuedAt) {
		return a.QueuedAt.After(b.QueuedAt)
	}
	ar, br := runStatusRank(a.Status), runStatusRank(b.Status)
	if ar != br {
		return ar > br
	}
	return runRecordTime(*a).After(runRecordTime(*b))
}

func (j *Jobs) list(topology string) []*controlplane.Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*controlplane.Run, 0, len(j.runs))
	for _, r := range j.runs {
		if topology == "" || r.Topology == topology {
			out = append(out, r)
		}
	}
	return out
}

// recoverableRuns returns terminal runs whose checkpoint journal may still hold
// unrecorded work: they failed or were interrupted after mutating the outside
// world but before recording the result in state.
//
// A running run is never recoverable — its journal is still being written and
// its state is still owned by the in-flight operation.
func (j *Jobs) recoverableRuns(topology string) []*controlplane.Run {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*controlplane.Run, 0)
	for _, r := range j.runs {
		if r.Status != controlplane.RunFailed || !r.Recoverable {
			continue
		}
		if topology == "" || r.Topology == topology {
			out = append(out, r)
		}
	}
	return out
}

func (j *Jobs) logWriter(runID string) *Broadcaster {
	return j.logs.Writer(runID)
}
