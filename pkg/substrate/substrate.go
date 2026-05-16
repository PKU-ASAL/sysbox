package substrate

import (
	"context"
	"io"
)

// Substrate is the contract every node provider must fulfill.
// A "substrate" (docker, firecracker, libvirt) creates and manages nodes,
// where a node is a unit of isolation running a guest (container, microVM, VM).
type Substrate interface {
	Name() string

	Capabilities() Capabilities

	PrepareImage(ctx context.Context, spec ImageSpec) (ImageRef, error)

	CreateNode(ctx context.Context, spec NodeSpec) (NodeHandle, error)

	StartNode(ctx context.Context, handle NodeHandle) error

	StopNode(ctx context.Context, handle NodeHandle) error

	DestroyNode(ctx context.Context, handle NodeHandle) error

	ExecInNode(ctx context.Context, handle NodeHandle, spec ExecSpec) (ExecResult, error)

	CopyToNode(ctx context.Context, handle NodeHandle, src, dst string) error

	CopyFromNode(ctx context.Context, handle NodeHandle, src, dst string) error

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
