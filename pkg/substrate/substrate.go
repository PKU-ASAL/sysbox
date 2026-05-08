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
}
