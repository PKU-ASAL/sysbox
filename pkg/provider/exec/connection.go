// Package exec provides a substrate-agnostic connection abstraction for
// provisioner execution. Each substrate implements the Connection interface;
// the provisioner runtime picks the right implementation based on the
// connection type declared in the HCL (or infers it from the substrate).
//
// Supported connection types:
//
//	"auto"   – inferred from substrate: docker → DockerConnection, vm → SSHConnection
//	"docker" – docker exec via Docker API (default for docker substrate)
//	"ssh"    – standard SSH (for VM/bare-metal substrates)
//	"vsock"  – virtio-vsock (Firecracker, Phase 3 stub)
package exec

import "context"

// Connection abstracts how the provisioner runtime reaches a running node.
type Connection interface {
	// ExecInline runs each line as a shell command (sh -c) sequentially.
	// Returns on first non-zero exit. stdout+stderr are streamed to the caller's logger.
	ExecInline(ctx context.Context, cmds []string) error

	// ExecBackground starts a command detached from the calling session.
	// Returns the PID of the spawned process (container-namespace PID for docker).
	ExecBackground(ctx context.Context, cmd []string, env map[string]string) (int, error)

	// CopyFile copies a local file into the node at dstPath.
	CopyFile(ctx context.Context, srcPath, dstPath string) error
}
