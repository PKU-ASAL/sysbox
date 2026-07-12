package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// ResourceHandler owns resource schema, plan semantics, and state lifecycle.
type ResourceHandler interface {
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

type CapabilityRequirement struct {
	Driver     string
	Capability driver.Capability
}
type CapabilityDeclarer interface {
	RequiredCapabilities(*graph.Node) ([]CapabilityRequirement, error)
}
type ResourceImporter interface {
	Import(context.Context, address.Address, string, string) (state.Resource, error)
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

type handlerRegistry struct {
	mu       sync.RWMutex
	handlers map[string]ResourceHandler
}

func newHandlerRegistry() *handlerRegistry {
	return &handlerRegistry{handlers: map[string]ResourceHandler{}}
}
func (r *handlerRegistry) Register(handler ResourceHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[handler.Type()]; exists {
		return fmt.Errorf("resource handler %q already registered", handler.Type())
	}
	r.handlers[handler.Type()] = handler
	return nil
}
func (r *handlerRegistry) Get(typ string) (ResourceHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[typ]
	return handler, ok
}

var resourceHandlers = newHandlerRegistry()

func RegisterResourceHandler(p ResourceHandler) {
	if err := resourceHandlers.Register(p); err != nil {
		panic(err)
	}
}

func GetResourceHandler(typ string) (ResourceHandler, bool) {
	return resourceHandlers.Get(typ)
}

func mustResourceHandler(typ string) ResourceHandler {
	p, ok := GetResourceHandler(typ)
	if !ok {
		panic(fmt.Sprintf("resource handler %q not registered", typ))
	}
	return p
}
