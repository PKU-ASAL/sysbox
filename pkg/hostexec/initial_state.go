package hostexec

import (
	"context"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/transport"
)

// Called under the ordinary execution lock, before preflight. An initial empty
// state is useful evidence only when there is no earlier execution whose effects
// could have been lost with its journal. Missing or ambiguous history fails shut.
func (b *Environment) initializeFirstApply(ctx context.Context, run *controlplane.Run) error {
	if run == nil {
		return fmt.Errorf("run required")
	}
	if run.Op != "apply" {
		return nil
	}
	repo, err := b.Repository(run.Topology)
	if err != nil {
		return err
	}
	st, err := repo.LoadState(ctx, run.Topology)
	if err != nil {
		return err
	}
	if st.Meta.Serial > 0 {
		return nil
	}
	grant, ok := transport.ParentExecutionGrantFromContext(ctx)
	if !ok || grant.Grant.Operation != "run_apply" || grant.Grant.AgentID != run.AgentID || grant.Grant.ActionID != run.ID || grant.Grant.Scope != (controlplane.ExecutionScope{TopologyUID: st.TopologyUID}) || grant.Grant.State != controlplane.GrantActive || !grant.Grant.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("%w: initial topology requires its live host execution grant", persistence.ErrConflict)
	}
	transactions := &controlstore.Transactions{Store: b.Store, ActionRepository: b.Repository}
	return transactions.InitializeFirstApply(ctx, *run, grant.Grant)
}
