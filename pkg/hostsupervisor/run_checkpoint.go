package hostsupervisor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/substrate"
)

type runCheckpointChild struct {
	Ref    TargetRef                         `json:"ref"`
	Handle substrate.ExecutionHandle         `json:"handle"`
	Grant  controlplane.SignedExecutionGrant `json:"grant"`
}

type runCheckpoint struct {
	Version  int                  `json:"version"`
	Request  RunRequest           `json:"request"`
	Run      controlplane.Run     `json:"run"`
	Children []runCheckpointChild `json:"children"`
}

type runCheckpointKey struct{}
type runCheckpointWriter func(context.Context, *RunChildren, *controlplane.Run) error

// CheckpointRunChildren is called only after all synchronous resource effects
// have returned successfully. It closes child admission. After it succeeds the
// trusted callback may only wait for or cancel those children, not start effects.
func CheckpointRunChildren(ctx context.Context, children *RunChildren, run *controlplane.Run) error {
	write, ok := ctx.Value(runCheckpointKey{}).(runCheckpointWriter)
	if !ok || children == nil || run == nil || run.Status != controlplane.RunDone {
		return fmt.Errorf("trusted successful resource execution required for checkpoint")
	}
	return write(ctx, children, run)
}

func (r *RunChildren) checkpointChildren() ([]runCheckpointChild, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	r.closed = true
	if r.starting != 0 || r.unknownAdmission {
		return nil, fmt.Errorf("child admission remains unconfirmed")
	}
	children := make([]runCheckpointChild, 0, len(r.children))
	for _, record := range r.children {
		if r.Dispatcher == nil || record.RunID == "" || record.RunID != r.Dispatcher.RunID {
			return nil, fmt.Errorf("child Run identity mismatch")
		}
		children = append(children, runCheckpointChild{record.Ref, record.Handle, record.Grant})
	}
	sort.Slice(children, func(i, j int) bool { return recordKey(children[i].Handle) < recordKey(children[j].Handle) })
	return children, nil
}

func (c *RunCoordinator) checkpointContext(ctx context.Context, req RunRequest, active *activeRun) context.Context {
	return context.WithValue(ctx, runCheckpointKey{}, runCheckpointWriter(func(_ context.Context, children *RunChildren, run *controlplane.Run) error {
		inventory, err := children.checkpointChildren()
		if err != nil {
			return err
		}
		copy := *run
		copy.Inputs, copy.Cleanup = nil, nil
		checkpoint := runCheckpoint{Version: 1, Request: req, Run: copy, Children: inventory}
		active.mu.Lock()
		defer active.mu.Unlock()
		if err := c.validateCheckpoint(checkpoint, active.grant); err != nil {
			return err
		}
		raw, err := json.Marshal(checkpoint)
		if err != nil {
			return err
		}
		gate, err := c.asyncGate(active.grant)
		if err != nil {
			return err
		}
		// Evidence persistence needs no new execution authority and must survive
		// parent cancellation, but never hold shutdown indefinitely.
		persist, cancel := context.WithTimeout(context.WithoutCancel(active.serviceCtx), 5*time.Second)
		defer cancel()
		return gate.CheckpointRun(persist, active.grant, req.ActionID, raw)
	}))
}

func (c *RunCoordinator) validateCheckpoint(cp runCheckpoint, parent controlplane.SignedExecutionGrant) error {
	if cp.Version != 1 || cp.Children == nil || cp.Run.ID != cp.Request.RunID || cp.Run.AgentID != c.AgentID || cp.Run.Op != cp.Request.Action || cp.Run.Status != controlplane.RunDone || cp.Run.Inputs != nil || cp.Run.Cleanup != nil {
		return effectgate.ErrUncertain
	}
	if err := c.verifyHistorical(cp.Request, parent); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, child := range cp.Children {
		g := child.Grant.Grant
		key := recordKey(child.Handle)
		if seen[key] || child.Handle.ID == "" || child.Ref.Topology != cp.Request.Topology || child.Ref.Scope != g.Scope || g.Scope.TopologyUID != parent.Grant.Scope.TopologyUID || g.ParentEpoch != parent.Grant.Epoch || g.AgentID != parent.Grant.AgentID || g.SessionID != parent.Grant.SessionID || g.CommandID != parent.Grant.CommandID || g.Attempt != parent.Grant.Attempt || g.Operation != "guest_task" {
			return effectgate.ErrFenced
		}
		if err := controlplane.VerifyExecutionGrant(c.IssuerKey, child.Grant, child.Grant.BootID, child.Grant.IssuedAt); err != nil {
			return err
		}
		seen[key] = true
	}
	return nil
}

// This lock spans admission, callback and result persistence. It is distinct
// from short journal locks, so renew/watchdog can continue while effects run.
// Never remove its file: another supervisor may already hold the same inode.
func (c *RunCoordinator) lockRunExecution(g controlplane.SignedExecutionGrant) (func(), bool, error) {
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return nil, false, err
	}
	name := fmt.Sprintf("executor-%x.lock", sha256.Sum256([]byte(runIdentity(g))))
	lock := flock.New(filepath.Join(c.Root, name))
	held, err := lock.TryLock()
	if err != nil || !held {
		_ = lock.Close()
		return nil, false, err
	}
	return func() { _ = lock.Close() }, true, nil
}

func (c *RunCoordinator) recoverCheckpoint(ctx context.Context, gate *effectgate.Gate, grant controlplane.ExecutionGrant, id string) error {
	prior, err := gate.ReadRunEnvelope(ctx, grant, id)
	if err != nil {
		return err
	}
	unlock, held, err := c.lockRunExecution(prior.Grant)
	if err != nil || !held {
		return err
	}
	defer unlock()
	// Completion or renewal may have raced the scan before the execution lock.
	prior, err = gate.ReadRun(ctx, prior.Grant, id)
	if err != nil {
		return err
	}
	if prior.State == effectgate.RunComplete {
		return nil
	}
	var cp runCheckpoint
	if len(prior.Checkpoint) == 0 || json.Unmarshal(prior.Checkpoint, &cp) != nil {
		return effectgate.ErrUncertain
	}
	if err := c.validateCheckpoint(cp, prior.Grant); err != nil {
		return err
	}
	if cp.Request.ActionID != id {
		return effectgate.ErrFenced
	}
	if len(cp.Children) != 0 {
		if c.Tasks == nil {
			return effectgate.ErrUncertain
		}
		dispatcher := *c.Tasks
		dispatcher.RunID = cp.Request.RunID
		var failures []error
		for _, child := range cp.Children {
			if err := ctx.Err(); err != nil {
				return err
			}
			call, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
			err := recoverCheckpointChild(call, &dispatcher, child)
			cancel()
			if err != nil {
				failures = append(failures, fmt.Errorf("child %s: %w", child.Handle.ID, err))
			}
		}
		if err := errors.Join(failures...); err != nil {
			return err
		}
	}
	cp.Run.Status = controlplane.RunFailed
	cp.Run.Err = "run controller stopped after resource execution; child termination confirmed"
	cp.Run.EndedAt = time.Now().UTC()
	raw, err := json.Marshal(cp.Run)
	if err != nil {
		return err
	}
	commit, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return gate.CommitQuiescentRun(commit, prior.Grant, id, raw)
}

func recoverCheckpointChild(ctx context.Context, dispatcher *Dispatcher, child runCheckpointChild) error {
	unlock, err := dispatcher.lock(ctx, child.Handle)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := dispatcher.load(child.Handle)
	if err != nil {
		return err
	}
	expected := child.Grant.Grant
	expected.ExpiresAt = current.Grant.Grant.ExpiresAt
	if current.RunID != dispatcher.RunID || current.Ref != child.Ref || current.Grant.Grant != expected {
		return effectgate.ErrFenced
	}
	if _, err := dispatcher.cancelLocked(ctx, current); err != nil {
		return err
	}
	// Reload the actual durable record while still holding its lock: only a
	// valid signed stop and its acknowledged report discharge this child.
	current, err = dispatcher.load(child.Handle)
	if err != nil {
		return err
	}
	if current.StopReceipt == nil || !current.Reported {
		return effectgate.ErrUncertain
	}
	return ctx.Err()
}
