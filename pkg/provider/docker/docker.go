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

var _ substrate.Substrate = (*Substrate)(nil)
