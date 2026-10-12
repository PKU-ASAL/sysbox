package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ResetSSHDependency is admitted with the node reset, before any destruction.
// Started distinguishes safe first execution from an uncertain guest mutation.
type ResetSSHDependency struct {
	Previous      state.Resource            `json:"previous"`
	DesiredDigest string                    `json:"desired_digest"`
	ActionID      string                    `json:"action_id"`
	Config        substrate.SSHAccessConfig `json:"config"`
	Started       bool                      `json:"started,omitempty"`
	Result        *state.Resource           `json:"result,omitempty"`
}

func resetSSHActionID(resetID, resource string) string {
	sum := sha256.Sum256([]byte("reset-ssh\x00" + resetID + "\x00" + resource))
	return hex.EncodeToString(sum[:])
}

func (e *Executor) pinResetSSH(record *ActionRecord) error {
	for _, current := range e.state.Resources {
		if current.Address.Type != "sysbox_ssh_access" {
			continue
		}
		saved, err := decodeSSHState(current, e.state)
		if err != nil {
			return err
		}
		if saved.Node != record.Resource.String() {
			continue
		}
		if record.Candidate == nil || saved.Handle.NodeID != record.Candidate.ExternalID || saved.Handle.ResourceUID != record.ResourceUID || saved.Handle.Generation != record.Generation-1 {
			return fmt.Errorf("reset SSH dependency has stale node identity: %s", current.Address)
		}
		n := e.graph.Get(current.Address)
		if n == nil {
			return fmt.Errorf("reset SSH dependency missing from configuration: %s", current.Address)
		}
		ref, desired, err := sshDesired(n, saved.Config.User, "validate")
		if err != nil || ref != saved.Node {
			return fmt.Errorf("reset SSH dependency target changed: %s", current.Address)
		}
		digest, err := desiredActionDigest(n, controlplane.PlannedChange{Address: current.Address, Action: controlplane.PlanActionCreate})
		if err != nil {
			return err
		}
		record.ResetSSH = append(record.ResetSSH, ResetSSHDependency{Previous: cloneResetStateResource(current), Config: desired.Config, DesiredDigest: digest, ActionID: resetSSHActionID(record.ID, current.Address.String())})
	}
	sort.Slice(record.ResetSSH, func(i, j int) bool {
		return record.ResetSSH[i].Previous.Address.String() < record.ResetSSH[j].Previous.Address.String()
	})
	return nil
}

// SameResetSSHIntent compares immutable admission data without mutable progress.
func SameResetSSHIntent(a, b ActionRecord) bool {
	if len(a.ResetSSH) != len(b.ResetSSH) {
		return false
	}
	for i, before := range a.ResetSSH {
		after := b.ResetSSH[i]
		before.Started, after.Started = false, false
		before.Result, after.Result = nil, nil
		x, _ := json.Marshal(before)
		y, _ := json.Marshal(after)
		if string(x) != string(y) {
			return false
		}
	}
	return true
}

// ValidateResetSSH checks dependencies against the still-canonical old state.
// Publication of the node and all verified child results happens in one commit.
func (a ActionRecord) ValidateResetSSH(st *state.State) error {
	if a.Action != controlplane.PlanActionReset {
		if len(a.ResetSSH) != 0 {
			return fmt.Errorf("SSH dependencies require reset")
		}
		return nil
	}
	seen := map[string]bool{}
	for _, dep := range a.ResetSSH {
		old := dep.Previous
		key := old.Address.String()
		if seen[key] || dep.ActionID != resetSSHActionID(a.ID, key) || dep.DesiredDigest == "" || old.Generation < 1 || old.Generation == int64(^uint64(0)>>1) {
			return fmt.Errorf("invalid reset SSH admission")
		}
		seen[key] = true
		current := st.FindResource(old.Address)
		if current == nil {
			return fmt.Errorf("reset SSH dependency disappeared")
		}
		x, _ := json.Marshal(current)
		y, _ := json.Marshal(old)
		if string(x) != string(y) {
			return fmt.Errorf("reset SSH dependency changed")
		}
		saved, err := decodeSSHState(old, st)
		if err != nil {
			return err
		}
		if saved.Node != a.Resource.String() || saved.Handle.ResourceUID != a.ResourceUID || saved.Handle.Generation != a.Generation-1 || old.OwnerUID != a.OwnerUID {
			return fmt.Errorf("reset SSH dependency owner mismatch")
		}
		if (substrate.SSHAccessMutation{ID: dep.ActionID, Config: dep.Config}).Validate() != nil || dep.Config.User != saved.Config.User || dep.Config.AccessID != saved.Config.AccessID {
			return fmt.Errorf("invalid reset SSH desired configuration")
		}
		if dep.Started && (!a.EffectComplete || a.Phase != "verifying") {
			return fmt.Errorf("SSH started before node verification")
		}
		if dep.Result == nil {
			if a.Status == ActionSucceeded {
				return fmt.Errorf("reset SSH result missing")
			}
			continue
		}
		if !dep.Started || a.Candidate == nil {
			return fmt.Errorf("reset SSH result has no admission")
		}
		r := dep.Result
		if !r.Address.Equal(old.Address) || r.OwnerUID != old.OwnerUID || r.ResourceUID != old.ResourceUID || r.Generation != old.Generation+1 {
			return fmt.Errorf("reset SSH result resource mismatch")
		}
		result, err := decodeSSHState(*r, st)
		if err != nil {
			return err
		}
		x, _ = json.Marshal(result.Config)
		y, _ = json.Marshal(dep.Config)
		if string(x) != string(y) {
			return fmt.Errorf("reset SSH result configuration mismatch")
		}
		if result.Node != a.Resource.String() || result.Handle.ID != dep.ActionID || result.Handle.NodeID != a.Candidate.ExternalID || result.Handle.ResourceUID != a.ResourceUID || result.Handle.Generation != a.Generation {
			return fmt.Errorf("reset SSH result node mismatch")
		}
	}
	for _, resource := range st.Resources {
		if resource.Address.Type != "sysbox_ssh_access" {
			continue
		}
		saved, err := decodeSSHState(resource, st)
		if err != nil {
			return err
		}
		if saved.Node == a.Resource.String() && !seen[resource.Address.String()] {
			return fmt.Errorf("reset omitted SSH dependency")
		}
	}
	return nil
}

func (e *Executor) reconcileResetSSH(ctx context.Context, record *ActionRecord) error {
	return e.reconcileResetSSHWithEffects(ctx, record, true)
}

func (e *Executor) reconcileResetSSHWithEffects(ctx context.Context, record *ActionRecord, allowCreate bool) error {
	if len(record.ResetSSH) == 0 {
		return nil
	}
	if record.Candidate == nil || !record.EffectComplete || record.Phase != "verifying" {
		return ErrActionUncertain
	}
	staged, err := cloneExecutionState(e.state)
	if err != nil {
		return err
	}
	staged.RemoveResource(record.Resource)
	staged.AddResource(cloneResetStateResource(*record.Candidate))
	worker := *e
	worker.state = staged
	h := SSHAccessResourceHandler{}
	persist := func() error {
		record.ExpectedSerial = e.state.Meta.Serial
		if err := e.actions.CommitActionOutcome(ctx, *record, nil); err != nil {
			return fmt.Errorf("persist reset SSH progress: %w", err)
		}
		record.Revision++
		return nil
	}
	for i := range record.ResetSSH {
		dep := &record.ResetSSH[i]
		n := e.graph.Get(dep.Previous.Address)
		if n == nil {
			return fmt.Errorf("reset SSH configuration missing")
		}
		digest, err := desiredActionDigest(n, controlplane.PlannedChange{Address: n.Address, Action: controlplane.PlanActionCreate})
		if err != nil || digest != dep.DesiredDigest {
			return fmt.Errorf("%w: reset SSH configuration changed", ErrPlanStale)
		}
		id := substrate.OperationIdentity{ActionID: dep.ActionID, OwnerUID: record.OwnerUID, TopologyUID: record.OwnerUID, ResourceUID: dep.Previous.ResourceUID, Generation: dep.Previous.Generation + 1}
		childCtx := substrate.WithOperationIdentity(ctx, id)
		if dep.Result != nil {
			observed, err := h.ReadWithState(childCtx, *dep.Result, staged)
			if err != nil {
				return err
			}
			if observed.Status != state.ResourcePresent {
				return ErrVerification
			}
			continue
		}
		found, err := h.Discover(childCtx, &ProviderContext{exec: &worker}, n)
		if err != nil {
			return err
		}
		var result state.Resource
		if found.Status == state.ResourcePresent {
			if !dep.Started {
				return fmt.Errorf("SSH result precedes reset child admission")
			}
			result = found.Resource
		} else if found.Status == state.ResourceAbsent && !dep.Started && allowCreate {
			dep.Started = true
			if err := persist(); err != nil {
				return err
			}
			result, err = h.Create(childCtx, &ProviderContext{exec: &worker}, n)
			if err != nil {
				return err
			}
		} else {
			return fmt.Errorf("%w: reset SSH child result unknown; refusing replay", ErrActionUncertain)
		}
		result.Dependencies = append(result.Dependencies, n.Deps...)
		dep.Result = &result
		if err := persist(); err != nil {
			return err
		}
	}
	return nil
}
