package substrate

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/hcl/v2"
)

// Substrate is the contract every node provider must fulfill.
//
// A "substrate" (docker, firecracker, libvirt) creates and manages nodes,
// where a node is a unit of isolation running a guest (container, microVM, VM).
//
// All substrates may embed BaseSubstrate to inherit safe defaults for
// Validate / DecodeProviderConfig / etc; only methods that have substrate-
// specific behaviour need to be overridden.
type Substrate interface {
	Name() string

	Capabilities() Capabilities

	// Validate is invoked during `sysbox plan` to reject specs the substrate
	// cannot honour (e.g. docker rejecting a NodeSpec with a kernel field).
	// Returning nil means "spec is acceptable to this substrate".
	Validate(spec NodeSpec) error

	// DecodeProviderConfig parses the substrate-specific `provider "X" {}`
	// HCL block into a substrate-owned typed value (e.g.
	// *dockerprovider.Config). The returned value is later attached to
	// NodeSpec.ProviderConfig and is only type-asserted inside the
	// substrate's own package. Substrates that have no provider block
	// return (nil, nil); callers should still pass a usable default into
	// NodeSpec.ProviderConfig.
	DecodeProviderConfig(body hcl.Body, ctx *hcl.EvalContext) (any, error)

	// Dependencies inspects a decoded provider config and reports the
	// resource references the runtime must resolve before CreateNode
	// (kernels, images, networks). Substrates with no cross-resource refs
	// return a zero-value ProviderDeps.
	Dependencies(cfg any) ProviderDeps

	PrepareImage(ctx context.Context, spec ImageSpec) (ImageRef, error)

	CreateNode(ctx context.Context, spec NodeSpec) (NodeHandle, error)

	StartNode(ctx context.Context, handle NodeHandle) error

	StopNode(ctx context.Context, handle NodeHandle) error

	DestroyNode(ctx context.Context, handle NodeHandle) error

	// Connection returns a providerexec.Connection for reaching the running
	// node. The substrate inspects NodeHandle.Conn (set by CreateNode or
	// populateConnInfo) and the optional HCL connection configs to pick the
	// right implementation (docker-exec, vsock-rpc, SSH, WinRM, ...).
	// Returns nil if no connection is available (e.g. node not running or
	// substrate has no control-plane channel).
	Connection(handle NodeHandle, conns []ConnectionHint) (Connection, error)

	// AttachNIC creates a network device for the node and wires it into the
	// topology described by the LinkRequest. Each substrate picks its own
	// device type (veth, tap, macvtap, ...), creates it, and attaches it.
	// Returns an AttachedNIC for runtime to persist in state.
	AttachNIC(ctx context.Context, handle NodeHandle, req LinkRequest) (AttachedNIC, error)

	ObservationHook(ctx context.Context, handle NodeHandle) (ObservationTarget, error)

	// NodeStatus reports whether the node is healthy (running and reachable).
	// Used by drift detection; a false result triggers a Change entry in the plan.
	NodeStatus(ctx context.Context, handle NodeHandle) (bool, error)

	// MarshalProviderState serialises NodeHandle.Provider to JSON for state
	// persistence. Returning (nil, nil) means "this substrate has no
	// provider-specific state to persist". Runtime stores the result in
	// state.Instance under the "provider_extra" key.
	MarshalProviderState(handle NodeHandle) (json.RawMessage, error)

	// UnmarshalProviderState reconstructs a substrate-owned typed value from
	// a previously persisted JSON blob, to be assigned to NodeHandle.Provider
	// when the substrate is invoked from a cold path (destroy, drift refresh).
	// Returning (nil, nil) is acceptable; substrates may also fall back to
	// reconstructing state from the bare NodeHandle.ID.
	UnmarshalProviderState(data json.RawMessage) (any, error)
}
