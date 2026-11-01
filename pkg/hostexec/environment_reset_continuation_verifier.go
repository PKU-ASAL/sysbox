package hostexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

// VerifyResetContinuation accepts committed signed stop evidence, never failed
// status or lease expiry alone. Callers hold execution exclusion; admission
// independently rechecks original actions and state serial in its transaction.
func (b *Environment) VerifyResetContinuation(ctx context.Context, run, parent *controlplane.Run) error {
	if run == nil || parent == nil || run.ParentID != parent.ID || run.Topology != parent.Topology || run.Op != "reset" || parent.Op != "reset" {
		return persistence.ErrConflict
	}
	original, err := b.Store.GetRun(ctx, parent.ID)
	if err != nil {
		return err
	}
	if original == nil || !controlplane.SameRunIncarnation(*original, *parent) || original.Attempt != parent.Attempt || !original.Status.IsTerminal() || original.HostCompletion == nil {
		return fmt.Errorf("reset continuation requires committed host completion evidence")
	}
	proof := original.HostCompletion
	if err := controlplane.VerifyRunStopReceipt(b.hostKey(original.AgentID), proof.Receipt, proof.Evidence); err != nil {
		return err
	}
	var observed controlplane.Run
	if json.Unmarshal(proof.Evidence, &observed) != nil || observed.HostCompletion != nil || len(observed.Inputs) != 0 || !controlplane.SameRunIncarnation(*original, observed) || original.Attempt != observed.Attempt || original.LeaseOwner != observed.LeaseOwner || original.Status != observed.Status || !original.EndedAt.Equal(observed.EndedAt) || original.Err != observed.Err {
		return persistence.ErrConflict
	}
	grant := proof.Receipt.Grant
	if grant.Operation != "run_reset" || grant.AgentID != original.AgentID || grant.ParentEpoch != 0 {
		return persistence.ErrConflict
	}
	repo, err := b.Repository(run.Topology)
	if err != nil {
		return err
	}
	st, err := repo.LoadState(ctx, run.Topology)
	if err != nil {
		return err
	}
	if grant.Scope.TopologyUID != st.TopologyUID {
		return persistence.ErrConflict
	}
	raw, err := json.Marshal(proof.Receipt)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: b.Store})
	return authority.RequireHostStopped(ctx, grant, hex.EncodeToString(hash[:]))
}
