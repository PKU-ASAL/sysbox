package substrate

import (
	"context"
	"io"

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
	// *dockerprovider.Config). The returned value is later embedded in
	// NodeSpec.Provider and is only type-asserted inside the substrate's
	// own package. Substrates that have no provider block return (nil, nil).
	//
	// W1-PR-05 will wire this into the HCL parser; PR-01 only freezes the
	// signature so providers can start implementing it.
	DecodeProviderConfig(body hcl.Body) (any, error)

	PrepareImage(ctx context.Context, spec ImageSpec) (ImageRef, error)

	CreateNode(ctx context.Context, spec NodeSpec) (NodeHandle, error)

	StartNode(ctx context.Context, handle NodeHandle) error

	StopNode(ctx context.Context, handle NodeHandle) error

	DestroyNode(ctx context.Context, handle NodeHandle) error

	// Deprecated: use Connection(handle, hint).ExecInline. Removed in W1-PR-06.
	ExecInNode(ctx context.Context, handle NodeHandle, spec ExecSpec) (ExecResult, error)

	// Deprecated: use Connection(handle, hint).CopyFile. Removed in W1-PR-06.
	CopyToNode(ctx context.Context, handle NodeHandle, src, dst string) error

	// Deprecated: not part of v1.0; removed in W1-PR-06.
	CopyFromNode(ctx context.Context, handle NodeHandle, src, dst string) error

	// Deprecated: use Console(handle, kind). Removed in W1-PR-06.
	AttachTTY(ctx context.Context, handle NodeHandle) (io.ReadWriteCloser, error)

	AttachNIC(ctx context.Context, handle NodeHandle, nic NIC) error

	ObservationHook(ctx context.Context, handle NodeHandle) (ObservationTarget, error)

	// NodeStatus reports whether the node is healthy (running and reachable).
	// Used by drift detection; a false result triggers a Change entry in the plan.
	NodeStatus(ctx context.Context, handle NodeHandle) (bool, error)
}

// DockerCapable is an optional interface that substrates can implement
// to expose Docker-specific operations. Runtime code should check for
// this interface with a type assertion rather than depending on the
// concrete *dockerprovider.Substrate type.
type DockerCapable interface {
	// ExecBackground starts a process inside the node and returns its PID.
	ExecBackground(ctx context.Context, handle NodeHandle, spec ExecSpec) (int, error)

	// GetContainerIP returns the first IPv4 address of the container.
	GetContainerIP(ctx context.Context, containerID string) (string, error)

	// ConnectContainerToNetwork attaches a running container to a Docker network.
	ConnectContainerToNetwork(ctx context.Context, containerID, networkID, ip string) error
}

// Verify Docker substrate satisfies DockerCapable at compile time.
// The actual check is in the docker provider package; this comment
// serves as documentation for implementers.
