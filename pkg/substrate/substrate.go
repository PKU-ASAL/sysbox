package substrate

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
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

	// PreflightChecks reports host/API-container prerequisites for this
	// substrate, such as Docker socket access, KVM, or required binaries.
	PreflightChecks(required bool) []PreflightCheck

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

	// CreateManagedNetwork creates a substrate-managed L2/L3 network segment
	// (Docker bridge, libvirt NAT network, ...) with internet access (NAT).
	// Returns a NetworkInfo describing the created network.
	// Substrates that don't support managed networks return ErrNotSupported.
	CreateManagedNetwork(ctx context.Context, spec ManagedNetworkSpec) (ManagedNetworkInfo, error)

	// RemoveManagedNetwork tears down a previously created managed network.
	RemoveManagedNetwork(ctx context.Context, id string) error

	// ReadManagedNetwork queries an existing managed network by name without
	// creating one. Returns ErrNotSupported if the substrate cannot inspect
	// networks. Used by data sources to reference existing infrastructure.
	ReadManagedNetwork(ctx context.Context, spec ManagedNetworkSpec) (ManagedNetworkInfo, error)

	// AttachNIC creates a network device for the node and wires it into the
	// topology described by the LinkRequest. Each substrate picks its own
	// device type (veth, tap, macvtap, ...), creates it, and attaches it.
	// Returns an AttachedNIC for runtime to persist in state.
	AttachNIC(ctx context.Context, handle NodeHandle, req LinkRequest) (AttachedNIC, error)

	// NodeStatus reports whether the node is healthy (running and reachable).
	// Used by drift detection; a false result triggers a Change entry in the plan.
	NodeStatus(ctx context.Context, handle NodeHandle) (bool, error)

	// ObserveNode returns a structured lifecycle observation for a node. This
	// is the common supervisor surface across daemon-backed substrates
	// (Docker/libvirt) and process-backed substrates (Firecracker/raw qemu).
	ObserveNode(ctx context.Context, handle NodeHandle) (NodeObservation, error)

	// AdoptNode reconnects a fresh control-plane process to a node that was
	// created earlier and still exists outside the current process memory.
	// Substrates with no takeover semantics return ErrNotSupported via
	// BaseSubstrate. Firecracker uses this to rebuild its in-memory vmStore
	// from persisted provider state after an API server restart.
	AdoptNode(ctx context.Context, handle NodeHandle) (NodeHandle, error)

	// PrepareHandle is called by runtime after NIC attachment and PrimaryIP
	// assignment, before provisioners run. The substrate may:
	//   - rewrite ProviderConfig fields (e.g. resolve kernel ref to local path)
	//   - populate handle.Conn (Kind, Endpoint, Auth)
	//   - populate substrate-specific HandleState fields (SSHIP, SSHPort, …)
	//
	// The StateReader gives read-only access to applied resources so that
	// substrates can resolve cross-resource references (e.g. sysbox_kernel →
	// local path) without importing pkg/state (import-cycle prevention).
	//
	// BaseSubstrate provides a no-op default.
	PrepareHandle(ctx context.Context, handle *NodeHandle, pc any, st StateReader) error

	// ReadNode queries the substrate for a node that exists outside of sysbox
	// state (e.g. a pre-existing container or VM). Returns a NodeHandle that
	// the runtime can store directly in state via `sysbox import`.
	// Returns ErrNotSupported if the substrate does not implement import.
	ReadNode(ctx context.Context, id string) (NodeHandle, error)

	// Pause suspends the node (container freeze / VM pause). Returns
	// ErrNotSupported if the substrate does not implement suspend.
	Pause(ctx context.Context, handle NodeHandle) error

	// Resume un-suspends a paused node.
	Resume(ctx context.Context, handle NodeHandle) error

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

// StateReader is a narrow read-only view of the runtime state that substrates
// may use inside PrepareHandle to resolve cross-resource references.
// It avoids a direct import of pkg/state (which would create an import cycle).
type StateReader interface {
	// ResourceInstance returns the instance map for a named resource, or nil
	// if the resource has not yet been applied.
	ResourceInstance(address.Address) map[string]any
}
