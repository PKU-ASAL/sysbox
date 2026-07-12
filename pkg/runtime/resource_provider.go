package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ResourceProvider is the target boundary for sysbox resource lifecycle
// implementations. Runtime schedules graph actions and state transactions,
// while each resource provider owns schema, diff, read, and CRUD.
type ResourceProvider interface {
	Type() string
	Schema() ResourceSchema
	Read(ctx context.Context, current state.Resource) (ResourceReadResult, error)
	PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error)
	Create(ctx context.Context, pc *ProviderContext, desired *graph.Node) (state.Resource, error)
	Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error
	ExternalID(current state.Resource) string
}

type ResourceGraphDecoder interface {
	DecodeResource(r config.ResourceBlock, name string, ctx *hcl.EvalContext) (data any, deps []address.Address, err error)
}

type DataGraphDecoder interface {
	DecodeData(d config.DataBlock, ctx *hcl.EvalContext) (data any, deps []address.Address, err error)
}

type ResourceReadResult struct {
	Status      state.ResourceStatus
	Resource    state.Resource
	Reason      string
	Decision    controlplane.RecoveryDecision
	Observation *substrate.NodeObservation
	Checks      map[string]controlplane.ResourceCheckHealth
}

func resourceReadOK(current state.Resource) ResourceReadResult {
	return ResourceReadResult{
		Resource: current,
		Status:   state.ResourcePresent,
		Decision: controlplane.RecoveryDecisionNoop,
	}
}

var (
	resourceProvidersMu sync.RWMutex
	resourceProviders   = map[string]ResourceProvider{}
)

func RegisterResourceProvider(p ResourceProvider) {
	resourceProvidersMu.Lock()
	defer resourceProvidersMu.Unlock()
	resourceProviders[p.Type()] = p
}

func GetResourceProvider(typ string) (ResourceProvider, bool) {
	resourceProvidersMu.RLock()
	defer resourceProvidersMu.RUnlock()
	p, ok := resourceProviders[typ]
	return p, ok
}

func mustResourceProvider(typ string) ResourceProvider {
	p, ok := GetResourceProvider(typ)
	if !ok {
		panic(fmt.Sprintf("resource provider %q not registered", typ))
	}
	return p
}
