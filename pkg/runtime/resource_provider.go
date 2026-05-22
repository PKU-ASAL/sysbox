package runtime

import (
	"context"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// ResourceProvider is the target boundary for sysbox resource lifecycle
// implementations. Runtime should eventually schedule graph actions and state
// transactions, while each resource provider owns schema, diff, read, and CRUD.
//
// The current executor still contains legacy switch-based dispatch. This
// interface is intentionally introduced first so new resource types can adopt
// the provider shape without forcing a high-risk migration of existing code.
type ResourceProvider interface {
	Type() string
	Schema() ResourceSchema
	Read(ctx context.Context, current state.Resource) (state.Resource, error)
	PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error)
	Create(ctx context.Context, exec *Executor, desired *graph.Node) (state.Resource, error)
	Update(ctx context.Context, exec *Executor, desired *graph.Node, current state.Resource) (state.Resource, error)
	Delete(ctx context.Context, exec *Executor, current state.Resource) error
}
