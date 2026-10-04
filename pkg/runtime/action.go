package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"time"
)

type ActionStatus string

const (
	ActionIntentCommitted ActionStatus = "intent_committed"
	ActionSucceeded       ActionStatus = "succeeded"
	ActionUncertain       ActionStatus = "uncertain"
	ActionBlocked         ActionStatus = "blocked"
)

// ActionRecord preserves authorized identity and the best available recovery
// evidence. A provider error is not proof that its effect did not happen.
type ActionRecord struct {
	RunQueuedAt    time.Time                   `json:"run_queued_at,omitempty"`
	RunRevision    string                      `json:"run_revision,omitempty"`
	Revision       int64                       `json:"revision"`
	ID             string                      `json:"id"`
	Topology       string                      `json:"topology"`
	RunID          string                      `json:"run_id"`
	Resource       address.Address             `json:"resource"`
	Action         controlplane.PlanActionType `json:"action"`
	Status         ActionStatus                `json:"status"`
	DesiredDigest  string                      `json:"desired_digest,omitempty"`
	OwnerUID       string                      `json:"owner_uid"`
	ResourceUID    string                      `json:"resource_uid"`
	Generation     int64                       `json:"generation"`
	StateSerial    int64                       `json:"state_serial"`
	ExpectedSerial int64                       `json:"expected_serial"`
	Candidate      *state.Resource             `json:"candidate,omitempty"`
	Error          string                      `json:"error,omitempty"`
	// ReplayForbidden means resource presence cannot prove that the complete
	// composite effect (guest initialization, provisioners, image entry) finished.
	ReplayForbidden bool   `json:"replay_forbidden,omitempty"`
	EffectComplete  bool   `json:"effect_complete,omitempty"`
	Phase           string `json:"phase,omitempty"`
}

// ReferenceOnlyResource manages a local reference, not the external object
// it names. Deletion verifies removal of that reference only.
type ReferenceOnlyResource interface{ ReferenceOnly() }

func requiresCompositeCompletion(resourceType string, action controlplane.PlanActionType) bool {
	return (action == controlplane.PlanActionCreate || action == controlplane.PlanActionReset) && (resourceType == "sysbox_node" || resourceType == "sysbox_router")
}

// ActionRepository owns the atomic journal/resource boundary. Intent writes
// reject an unresolved action for the same topology/resource. Outcome writes
// compare ownership and state serial, then commit journal and patch together.
type ActionRepository interface {
	LoadActions(context.Context, string) ([]ActionRecord, error)
	LoadState(context.Context, string) (*state.State, error)
	CommitActionIntent(context.Context, ActionRecord) error
	CommitActionOutcome(context.Context, ActionRecord, *StatePatch) error
}

// ResourceDiscoverer must make a bounded external query. State absence alone
// is never evidence that creation is safe. Present results require a complete
// managed identity; ambiguous/unowned matches return unknown or an error.
type ResourceDiscoverer interface {
	Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error)
}

func (e *Executor) SetActionRepository(repository ActionRepository) { e.actions = repository }
