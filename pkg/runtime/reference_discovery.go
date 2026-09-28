package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func (ImageResourceHandler) ReferenceOnly()       {}
func (KernelResourceHandler) ReferenceOnly()      {}
func (DataNodeResourceHandler) ReferenceOnly()    {}
func (DataNetworkResourceHandler) ReferenceOnly() {}
func (DataImageResourceHandler) ReferenceOnly()   {}

// Artifact resolution and data reads do not claim ownership of the underlying
// object. The managed resource here is the persisted reference itself.
func absentReference() (ResourceReadResult, error) {
	return ResourceReadResult{Status: state.ResourceAbsent, Reason: "local reference not recorded"}, nil
}
func (ImageResourceHandler) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return absentReference()
}
func (KernelResourceHandler) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return absentReference()
}
func (DataNodeResourceHandler) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return absentReference()
}
func (DataNetworkResourceHandler) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return absentReference()
}
func (DataImageResourceHandler) Discover(context.Context, *ProviderContext, *graph.Node) (ResourceReadResult, error) {
	return absentReference()
}
