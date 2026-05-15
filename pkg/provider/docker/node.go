package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	// Idempotent: if a container with the same name already exists (leftover
	// from a partial previous apply where wireLink/AttachNIC failed after
	// CreateNode succeeded), reuse it instead of failing on a name conflict.
	if existing, err := s.cli.ContainerInspect(ctx, spec.Name); err == nil {
		return substrate.NodeHandle{
			ID: existing.ID,
			Attributes: map[string]any{
				"container_name": spec.Name,
			},
		}, nil
	}

	envs := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envs = append(envs, fmt.Sprintf("%s=%s", k, v))
	}

	hostCfg := &container.HostConfig{
		CapAdd:     []string{"NET_ADMIN"},
		Sysctls:    spec.Sysctls,
		Privileged: spec.Privileged,
		Binds:      spec.Binds,
	}
	if spec.PidMode != "" {
		hostCfg.PidMode = container.PidMode(spec.PidMode)
	}
	if spec.CgroupnsMode != "" {
		hostCfg.CgroupnsMode = container.CgroupnsMode(spec.CgroupnsMode)
	}

	// Network mode strategy:
	//   - No Docker networks needed  → NetworkMode:"none" (fully isolated netns,
	//     veth pairs injected manually later).
	//   - One or more Docker bridge networks needed → attach the first one via
	//     NetworkingConfig at create time (avoids the "cannot connect to multiple
	//     networks with one in none-mode" error); extras are connected after start.
	netCfg := &network.NetworkingConfig{}
	if len(spec.InitialDockerNets) == 0 {
		hostCfg.NetworkMode = "none"
	} else {
		first := spec.InitialDockerNets[0]
		ip := trimCIDR(first.IPv4)
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
			first.NetworkID: {
				IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip},
			},
		}
	}

	resp, err := s.cli.ContainerCreate(ctx,
		&container.Config{
			Image: spec.Image.Repository,
			Env:   envs,
			// Explicitly override ENTRYPOINT so images with their own default
			// (e.g. aquasec/tracee) stay alive for provisioner exec calls.
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{"sleep infinity"},
		},
		hostCfg,
		netCfg,
		nil,
		spec.Name,
	)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("create container: %w", err)
	}

	return substrate.NodeHandle{
		ID: resp.ID,
		Attributes: map[string]any{
			"container_name": spec.Name,
		},
	}, nil
}

func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerStart(ctx, h.ID, container.StartOptions{})
}

func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	timeoutSec := 10
	return s.cli.ContainerStop(ctx, h.ID, container.StopOptions{Timeout: &timeoutSec})
}

func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true})
}

// trimCIDR strips the prefix length from an IP/CIDR string, returning just the
// host address. Docker's IPAM config expects a plain IP, not CIDR notation.
func trimCIDR(cidr string) string {
	for i, c := range cidr {
		if c == '/' {
			return cidr[:i]
		}
	}
	return cidr
}
