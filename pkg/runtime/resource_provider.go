package runtime

import (
	"context"
	"errors"
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
	PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlanAction, error)
	Create(ctx context.Context, pc *ProviderContext, desired *graph.Node) (state.Resource, error)
	Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, current state.Resource) (state.Resource, error)
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
	Resource    state.Resource
	Reason      string
	Decision    controlplane.RecoveryDecision
	Observation *substrate.NodeObservation
	Checks      map[string]controlplane.ResourceCheckHealth
}

func resourceReadOK(current state.Resource) ResourceReadResult {
	return ResourceReadResult{
		Resource: current,
		Decision: controlplane.RecoveryDecisionNoop,
	}
}

type ResourceReadStatus string

const (
	ResourceReadDrifted ResourceReadStatus = "drifted"
	ResourceReadUnknown ResourceReadStatus = "unknown"
)

type ResourceReadError struct {
	Status ResourceReadStatus
	Reason string
	Err    error
}

func (e *ResourceReadError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil && e.Reason != "" {
		return e.Reason + ": " + e.Err.Error()
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Reason
}

func (e *ResourceReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func driftedResource(reason string) error {
	return &ResourceReadError{Status: ResourceReadDrifted, Reason: reason}
}

func unknownResource(reason string, err error) error {
	return &ResourceReadError{Status: ResourceReadUnknown, Reason: reason, Err: err}
}

func classifyResourceReadError(err error) (ResourceReadStatus, string, bool) {
	var readErr *ResourceReadError
	if !errors.As(err, &readErr) {
		return ResourceReadUnknown, err.Error(), false
	}
	reason := readErr.Reason
	if reason == "" && readErr.Err != nil {
		reason = readErr.Err.Error()
	}
	return readErr.Status, reason, true
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
