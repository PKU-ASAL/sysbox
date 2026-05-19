package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// HandleState is the docker-substrate's typed NodeHandle.Provider payload.
// Persisted via MarshalProviderState; reconstructed on cold-destroy.
type HandleState struct {
	ContainerName   string   `json:"container_name,omitempty"`
	ImageCmd        []string `json:"image_cmd,omitempty"`        // original image CMD (e.g. ["nginx", "-g", "daemon off;"])
	ImageEntrypoint []string `json:"image_entrypoint,omitempty"` // original image ENTRYPOINT
}

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	// If a container with this name exists (leftover from a partial previous
	// apply), force-remove it. Reusing a partially-wired container would
	// cause interface rename collisions on the next attach attempt.
	if _, err := s.cli.ContainerInspect(ctx, spec.Name); err == nil {
		fmt.Printf("[docker] warning: force-removing stale container %q\n", spec.Name)
		_ = s.cli.ContainerRemove(ctx, spec.Name, container.RemoveOptions{Force: true})
	}

	envs := util.EnvToSlice(spec.Env)

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
	//   - No NAT links in InitialLinks → NetworkMode:"none" (fully isolated netns,
	//     veth pairs injected manually later via AttachNIC).
	//   - One or more NAT links → attach the first via NetworkingConfig at create
	//     time (avoids the "cannot connect to multiple networks with none-mode" error);
	//     extras are hot-connected post-start via AttachNIC.
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
		// Set NetworkMode to the custom network so Docker does NOT also attach
		// the default bridge. Without this, Docker adds an extra eth0 (default
		// bridge) before our NAT network, pushing all interface indices up by 1
		// and breaking the vethIdx accounting in resource_node / router.
		hostCfg.NetworkMode = container.NetworkMode(first.DockerNetID)
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
			first.DockerNetID: {
				IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip},
			},
		}
	}

	// Inspect the image to capture its original CMD/Entrypoint so we can
	// exec them after provisioners finish (our container overrides them
	// with "sleep infinity" to keep the container alive for provisioning).
	var imageCmd []string
	var imageEntrypoint []string
	if imgInfo, _, err := s.cli.ImageInspectWithRaw(ctx, spec.Image.Repository); err == nil {
		imageCmd = imgInfo.Config.Cmd
		imageEntrypoint = imgInfo.Config.Entrypoint
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
		Provider: &HandleState{
			ContainerName:   spec.Name,
			ImageCmd:        imageCmd,
			ImageEntrypoint: imageEntrypoint,
		},
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

func (s *Substrate) Pause(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerPause(ctx, h.ID)
}

func (s *Substrate) Resume(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerUnpause(ctx, h.ID)
}

// ReadNode inspects an existing Docker container by name or ID and returns
// a NodeHandle suitable for importing into sysbox state.
func (s *Substrate) ReadNode(ctx context.Context, id string) (substrate.NodeHandle, error) {
	inspect, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("docker: container %q not found: %w", id, err)
	}
	hs := &HandleState{ContainerName: inspect.Name}
	if len(inspect.Name) > 0 && inspect.Name[0] == '/' {
		hs.ContainerName = inspect.Name[1:]
	}
	var primaryIP string
	for _, net := range inspect.NetworkSettings.Networks {
		if net.IPAddress != "" {
			primaryIP = net.IPAddress
			break
		}
	}
	return substrate.NodeHandle{
		ID:       inspect.ID[:12],
		Provider: hs,
		Conn:     substrate.ConnInfo{Kind: substrate.ConnKindDocker},
		Net:      substrate.NetInfo{PrimaryIP: primaryIP},
	}, nil
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
