// Package docker implements the Substrate interface using the Docker daemon.
package docker

import (
	"github.com/docker/docker/client"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Substrate is the Docker implementation of substrate.Substrate.
type Substrate struct {
	cli *client.Client
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
		BootTime:        "ms",
		NICType:         "veth",
	}
}

var _ substrate.Substrate = (*Substrate)(nil)
