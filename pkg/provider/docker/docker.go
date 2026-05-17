// Package docker implements the Substrate interface using the Docker daemon.
package docker

import (
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

// Validate rejects NodeSpecs that depend on hypervisor-only features.
func (s *Substrate) Validate(spec substrate.NodeSpec) error {
	if spec.Kernel != "" {
		return substrate.NewValidationError("docker substrate does not accept the kernel field (containers share the host kernel)")
	}
	if spec.Rootfs != "" {
		return substrate.NewValidationError("docker substrate does not accept the rootfs field; use image instead")
	}
	if spec.ChainInit != "" {
		return substrate.NewValidationError("docker substrate does not accept the chain_init field (no sysbox-init in containers)")
	}
	return nil
}

var _ substrate.Substrate = (*Substrate)(nil)
