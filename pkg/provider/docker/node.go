package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/oslab/sysbox/pkg/substrate"
)

// HandleState is the docker-substrate's typed NodeHandle.Provider payload.
// Persisted via MarshalProviderState; reconstructed on cold-destroy.
type HandleState struct {
	ContainerName string `json:"container_name,omitempty"`
}

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	// Idempotent: if a container with the same name already exists (leftover
	// from a partial previous apply where wireLink/AttachNIC failed after
	// CreateNode succeeded), reuse it instead of failing on a name conflict.
	if existing, err := s.cli.ContainerInspect(ctx, spec.Name); err == nil {
		return substrate.NodeHandle{
			ID:       existing.ID,
			Provider: &HandleState{ContainerName: spec.Name},
			Conn: substrate.ConnInfo{
				Kind:     substrate.ConnKindDocker,
				Endpoint: existing.ID,
			},
		}, nil
	}

	envs := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envs = append(envs, fmt.Sprintf("%s=%s", k, v))
	}

	pc, _ := spec.ProviderConfig.(*Config)
	if pc == nil {
		pc = &Config{}
	}

	hostCfg := &container.HostConfig{
		CapAdd:     []string{"NET_ADMIN"},
		Sysctls:    spec.Sysctls,
		Privileged: pc.Privileged,
		Binds:      pc.Binds,
	}
	if pc.PidMode != "" {
		hostCfg.PidMode = container.PidMode(pc.PidMode)
	}
	if pc.CgroupnsMode != "" {
		hostCfg.CgroupnsMode = container.CgroupnsMode(pc.CgroupnsMode)
	}

	// Network mode strategy:
	//   - No Docker networks needed  → NetworkMode:"none" (fully isolated netns,
	//     veth pairs injected manually later).
	//   - One or more Docker bridge networks needed → attach the first one via
	//     NetworkingConfig at create time (avoids the "cannot connect to multiple
	//     networks with one in none-mode" error); extras are connected after start.
	// Collect Docker NAT links from InitialLinks.
	var natLinks []substrate.LinkRequest
	for _, l := range spec.InitialLinks {
		if l.KindHint == substrate.NICKindDockerNAT || l.DockerNetID != "" {
			natLinks = append(natLinks, l)
		}
	}

	netCfg := &network.NetworkingConfig{}
	if len(natLinks) == 0 {
		hostCfg.NetworkMode = "none"
	} else {
		first := natLinks[0]
		ip := trimCIDR(first.IP)
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
			first.DockerNetID: {
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
		ID:       resp.ID,
		Provider: &HandleState{ContainerName: spec.Name},
		Conn: substrate.ConnInfo{
			Kind:     substrate.ConnKindDocker,
			Endpoint: resp.ID,
		},
	}, nil
}

// MarshalProviderState writes the docker HandleState as JSON. Persisted
// alongside the NodeHandle.ID in sysbox state so cold-destroy can reuse the
// container name.
func (s *Substrate) MarshalProviderState(h substrate.NodeHandle) (json.RawMessage, error) {
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs == nil {
		return nil, nil
	}
	return json.Marshal(hs)
}

// UnmarshalProviderState restores HandleState from a previously persisted blob.
func (s *Substrate) UnmarshalProviderState(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var hs HandleState
	if err := json.Unmarshal(data, &hs); err != nil {
		return nil, fmt.Errorf("docker: unmarshal handle state: %w", err)
	}
	return &hs, nil
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
