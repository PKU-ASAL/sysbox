// Package docker implements the Substrate interface using the Docker daemon.
package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/docker/docker/client"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Substrate is the Docker implementation of substrate.Substrate.
type Substrate struct {
	substrate.BaseSubstrate // inherits Validate / DecodeProviderConfig defaults
	cli                     *client.Client
}

// New connects to the local Docker daemon.
func New() (*Substrate, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &Substrate{cli: cli}, nil
}

func (s *Substrate) Name() string { return "docker" }

func (s *Substrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		SharedKernel:    true,
		SupportsWindows: false,
		NICHotPlug:      true,
		DiskHotPlug:     false,
		NICKinds:        []string{substrate.NICKindVeth},
		ConsoleKinds:    []string{substrate.ConsoleKindTTY},
		NeedsCloudinit:  false,
		PIDVisibility:   substrate.PIDVisibilityHost,
		SupportsPause:   true, // docker pause/unpause
		BootTime:        100 * time.Millisecond,
		Notes:           "Linux container; shares host kernel; eBPF works only with privileged + cap_sys_admin.",
	}
}

// Validate rejects NodeSpecs whose provider config carries hypervisor-only
// fields. With v1.0 the substrate-specific fields live in a `provider "docker"
// {}` block, so docker simply rejects any non-docker provider config.
func (s *Substrate) Validate(spec substrate.NodeSpec) error {
	if spec.ProviderConfig != nil {
		if _, ok := spec.ProviderConfig.(*Config); !ok {
			return substrate.NewValidationError(
				"docker substrate received provider config of type %T; expected *docker.Config",
				spec.ProviderConfig)
		}
	}
	return nil
}

// Connection returns a DockerConnection for reaching the container.
func (s *Substrate) Connection(handle substrate.NodeHandle, _ []substrate.ConnectionHint) (substrate.Connection, error) {
	return &dockerConn{sub: s, handle: handle}, nil
}

// dockerConn adapts the docker Substrate's own ExecInNode/CopyToNode/ExecBackground
// methods to the substrate.Connection interface.
type dockerConn struct {
	sub    *Substrate
	handle substrate.NodeHandle
}

func (c *dockerConn) ExecInline(ctx context.Context, cmds []string) error {
	return c.ExecStream(ctx, cmds, os.Stdout, os.Stderr)
}

func (c *dockerConn) ExecStream(ctx context.Context, cmds []string, stdout, stderr io.Writer) error {
	for _, cmd := range cmds {
		result, err := c.sub.ExecInNode(ctx, c.handle, substrate.ExecSpec{
			Cmd: []string{"sh", "-c", cmd},
		})
		if err != nil {
			return fmt.Errorf("exec %q: %w", cmd, err)
		}
		if result.Stdout != "" {
			fmt.Fprint(stdout, result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(stderr, result.Stderr)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("exec %q: exit code %d", cmd, result.ExitCode)
		}
	}
	return nil
}

func (c *dockerConn) ExecBackground(ctx context.Context, cmd []string, env map[string]string) (int, error) {
	pid, err := c.sub.ExecBackground(ctx, c.handle, substrate.ExecSpec{
		Cmd: cmd,
		Env: env,
	})
	if err != nil {
		return 0, fmt.Errorf("exec background %v: %w", cmd, err)
	}
	return pid, nil
}

func (c *dockerConn) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	return c.sub.CopyToNode(ctx, c.handle, srcPath, dstPath)
}

var _ substrate.Substrate = (*Substrate)(nil)
