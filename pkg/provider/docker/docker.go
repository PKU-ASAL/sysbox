// Package docker implements the Substrate interface using the Docker daemon.
package docker

import (
	"context"
	"fmt"
	"io"
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

// Close releases the Docker client's idle connections and goroutines.
// Should be called when the substrate is no longer needed.
func (s *Substrate) Close() error {
	if s.cli != nil {
		return s.cli.Close()
	}
	return nil
}

func (s *Substrate) Name() string { return "docker" }

func (s *Substrate) PreflightChecks(required bool) []substrate.PreflightCheck {
	return substrate.DockerPreflightChecks(required)
}

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
		PortExposures:   []string{substrate.PortExposureNone, substrate.PortExposureDirect, substrate.PortExposureHost},
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

func (c *dockerConn) Exec(ctx context.Context, req substrate.ExecRequest, stdout, stderr io.Writer) (substrate.ExecResult, error) {
	result, err := c.sub.ExecInNode(ctx, c.handle, req)
	if err != nil {
		return result, err
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, result.Stdout)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, result.Stderr)
	}
	return result, nil
}

func (c *dockerConn) ExecBackground(ctx context.Context, req substrate.ExecRequest) (int, error) {
	pid, err := c.sub.ExecBackground(ctx, c.handle, req)
	if err != nil {
		return 0, fmt.Errorf("exec background %s: %w", req.Program, err)
	}
	return pid, nil
}

func (c *dockerConn) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	return c.sub.CopyToNode(ctx, c.handle, srcPath, dstPath)
}
