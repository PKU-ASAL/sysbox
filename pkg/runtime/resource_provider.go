package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
