package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// HandleState is the docker-substrate's typed NodeHandle.Provider payload.
// Persisted via MarshalProviderState; reconstructed on cold-destroy.
type HandleState struct {
	ContainerName       string   `json:"container_name,omitempty"`
	ImageCmd            []string `json:"image_cmd,omitempty"`        // effective image CMD
	ImageEntrypoint     []string `json:"image_entrypoint,omitempty"` // effective image ENTRYPOINT
	DirectLaunch        bool     `json:"direct_launch,omitempty"`
	RemoveDefaultBridge bool     `json:"remove_default_bridge,omitempty"`
}

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	return s.createNode(ctx, spec, false)
}

func (s *Substrate) createNode(ctx context.Context, spec substrate.NodeSpec, strictName bool) (substrate.NodeHandle, error) {
	identity, managed := substrate.OperationIdentityFromContext(ctx)
	if !managed || identity.Validate() != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "create requires durable operation identity", nil)
	}
	labels, err := expectedLabels(ctx, spec.Labels, true)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if existing, err := s.cli.ContainerInspect(ctx, spec.Name); err == nil {
		if strictName || !managed || existing.Config == nil {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", fmt.Sprintf("container name %q already exists; refusing replacement", spec.Name), nil)
		}
		if err := verifyLabels(existing.Config.Labels, operationLabels(identity)); err != nil {
			return substrate.NodeHandle{}, err
		}
		return managedHandle(existing, identity.Generation)
	} else if !errdefs.IsNotFound(err) {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, "docker", "container name inspection unavailable", err)
	}

	envs := util.EnvToSlice(spec.Env)

	pc, _ := spec.ProviderConfig.(*Config)
	if pc == nil {
		pc = &Config{}
	}

	binds, err := normalizeBinds(pc.Binds)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	hostCfg := &container.HostConfig{
		CapAdd:     []string{"NET_ADMIN"},
		Sysctls:    spec.Sysctls,
		Privileged: pc.Privileged,
		Binds:      binds,
	}
	if pc.PidMode != "" {
		hostCfg.PidMode = container.PidMode(pc.PidMode)
	}
	if pc.CgroupnsMode != "" {
		hostCfg.CgroupnsMode = container.CgroupnsMode(pc.CgroupnsMode)
	}
	// vcpus and memory land as Docker resource limits. vcpus maps to --cpus (a
	// CFS quota — a soft limit that still lets the process "see" every host
	// core), memory to --memory in bytes. cpuset_cpus is the explicit affinity
	// escape hatch for images whose runtime reads the visible core count and
	// ignores CFS quota (e.g. old Java 8u181).
	if spec.VCPUs > 0 {
		hostCfg.NanoCPUs = int64(spec.VCPUs) * 1e9
	}
	if spec.Memory != "" {
		mem, err := parseMemoryMiB(spec.Memory)
		if err != nil {
			return substrate.NodeHandle{}, fmt.Errorf("docker: invalid memory %q: %w", spec.Memory, err)
		}
		hostCfg.Memory = mem * 1024 * 1024
	}
	if pc.CpusetCpus != "" {
		hostCfg.CpusetCpus = pc.CpusetCpus
	}
	exposedPorts, portBindings, err := dockerPortConfig(spec.Ports)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if len(portBindings) > 0 {
		hostCfg.PortBindings = portBindings
	}

	// Host port bindings require Docker's default bridge during create. The
	// first declared NAT attachment replaces it through the NIC capability.
	netCfg := &network.NetworkingConfig{}
	removeDefaultBridge := len(portBindings) > 0 || spec.ManagedNetwork
	if !removeDefaultBridge {
		hostCfg.NetworkMode = "none"
	}

	// Inspect the image to capture its original CMD/Entrypoint so we can
	// exec them after provisioners finish (our container overrides them
	// with "sleep infinity" to keep the container alive for provisioning).
	imgInfo, _, err := s.cli.ImageInspectWithRaw(ctx, spec.Image.ID)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("docker inspect image launch metadata: %w", err)
	}
	var imageCmd, imageEntrypoint []string
	if imgInfo.Config != nil {
		imageEntrypoint, imageCmd = effectiveLaunch(imgInfo.Config.Entrypoint, imgInfo.Config.Cmd, pc)
	} else {
		imageEntrypoint, imageCmd = effectiveLaunch(nil, nil, pc)
	}

	launch, direct := launchConfig(imageEntrypoint, imageCmd, pc)
	hs := &HandleState{ContainerName: spec.Name, ImageCmd: imageCmd, ImageEntrypoint: imageEntrypoint, RemoveDefaultBridge: removeDefaultBridge, DirectLaunch: direct}
	if managed {
		raw, err := json.Marshal(hs)
		if err != nil {
			return substrate.NodeHandle{}, err
		}
		labels["sysbox.node_state"] = string(raw)
	}
	resp, err := s.cli.ContainerCreate(ctx,
		&container.Config{
			Image:        spec.Image.ID,
			Env:          envs,
			Labels:       labels,
			ExposedPorts: exposedPorts,
			// Explicitly override ENTRYPOINT so images with their own default
			// (e.g. aquasec/tracee) stay alive for provisioner exec calls.
			Entrypoint: launch.Entrypoint,
			Cmd:        launch.Cmd,
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
		ID:         resp.ID,
		Generation: identity.Generation,
		Provider:   hs,
		Conn: substrate.ConnInfo{
			Kind:     substrate.ConnKindDocker,
			Endpoint: resp.ID,
		},
	}, nil
}

// launchConfig keeps the historical shell hold-open mode for images without
// an entrypoint, while allowing images such as Juice Shop that do not contain
// /bin/sh to start with their native image command.
func launchConfig(entrypoint, command []string, cfg *Config) (container.Config, bool) {
	if len(entrypoint) > 0 {
		return container.Config{Entrypoint: append([]string(nil), entrypoint...), Cmd: append([]string(nil), command...)}, true
	}
	return container.Config{Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{"sleep infinity"}}, false
}

func normalizeBinds(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, bind := range in {
		parts := strings.SplitN(bind, ":", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("docker: invalid bind %q", bind)
		}
		source := parts[0]
		if !filepath.IsAbs(source) {
			abs, err := filepath.Abs(source)
			if err != nil {
				return nil, fmt.Errorf("docker: resolve bind %q: %w", bind, err)
			}
			source = abs
		}
		out = append(out, source+":"+parts[1])
	}
	return out, nil
}

func dockerPortConfig(ports []substrate.PortSpec) (nat.PortSet, nat.PortMap, error) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range ports {
		exposure := p.Exposure
		if exposure == "" {
			exposure = substrate.PortExposureDirect
		}
		if exposure != substrate.PortExposureHost {
			continue
		}
		if p.Target <= 0 {
			return nil, nil, fmt.Errorf("docker port %q: target must be positive", p.Name)
		}
		if p.Published <= 0 {
			return nil, nil, fmt.Errorf("docker port %q: published must be positive for host exposure", p.Name)
		}
		port, err := nat.NewPort(dockerPortProtocol(p.Protocol), fmt.Sprintf("%d", p.Target))
		if err != nil {
			return nil, nil, fmt.Errorf("docker port %q: %w", p.Name, err)
		}
		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], nat.PortBinding{
			HostIP:   p.HostIP,
			HostPort: fmt.Sprintf("%d", p.Published),
		})
	}
	return exposed, bindings, nil
}

func dockerPortProtocol(protocol string) string {
	switch protocol {
	case "udp":
		return "udp"
	default:
		return "tcp"
	}
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
	if err := s.validateManagedHandle(ctx, &h); err != nil {
		return err
	}
	return s.cli.ContainerStart(ctx, h.ID, container.StartOptions{})
}

func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	if err := s.validateManagedHandle(ctx, &h); err != nil {
		return err
	}
	timeoutSec := 10
	return s.cli.ContainerStop(ctx, h.ID, container.StopOptions{Timeout: &timeoutSec})
}

func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	if err := s.validateManagedHandle(ctx, &h); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	err := s.cli.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *Substrate) Pause(ctx context.Context, h substrate.NodeHandle) error {
	if err := s.validateManagedHandle(ctx, &h); err != nil {
		return err
	}
	return s.cli.ContainerPause(ctx, h.ID)
}

func (s *Substrate) Resume(ctx context.Context, h substrate.NodeHandle) error {
	if err := s.validateManagedHandle(ctx, &h); err != nil {
		return err
	}
	return s.cli.ContainerUnpause(ctx, h.ID)
}

func (s *Substrate) validateManagedHandle(ctx context.Context, h *substrate.NodeHandle) error {
	identity, hasIdentity := substrate.OperationIdentityFromContext(ctx)
	if h.Generation == 0 && !hasIdentity {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "managed generation or operation identity required", nil)
	}
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return err
	}
	if ins.Config == nil {
		return fmt.Errorf("docker managed handle lacks ownership metadata")
	}
	labels := ins.Config.Labels
	if labels["sysbox.managed"] != "true" || labels["sysbox.owner_uid"] == "" || labels["sysbox.resource_uid"] == "" || labels["sysbox.topology_uid"] == "" {
		return fmt.Errorf("docker managed handle lacks complete ownership labels")
	}
	if hasIdentity {
		if err := identity.Validate(); err != nil {
			return err
		}
		if err := verifyLabels(ins.Config.Labels, operationLabels(identity)); err != nil {
			return err
		}
		if h.Generation != 0 && identity.Generation != h.Generation {
			return fmt.Errorf("docker generation conflict")
		}
	}
	if raw := ins.Config.Labels["sysbox.generation"]; h.Generation != 0 && raw != strconv.FormatInt(h.Generation, 10) {
		return fmt.Errorf("docker generation conflict")
	}
	if ins.ID == "" {
		return fmt.Errorf("docker inspection missing immutable ID")
	}
	h.ID = ins.ID
	return nil
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

// ExecImageEntry implements substrate.ImageEntryStarter: it launches the
// image's original CMD/Entrypoint inside the running container. CreateNode
// overrides the entrypoint with "sleep infinity" so provisioners can run
// first; the runtime calls this after provisioning so services (nginx,
// postgres, ...) actually start.
func (s *Substrate) ExecImageEntry(ctx context.Context, handle substrate.NodeHandle) error {
	hs, ok := handle.Provider.(*HandleState)
	if !ok || hs == nil {
		return nil
	}
	if hs.DirectLaunch {
		return nil
	}
	if len(hs.ImageEntrypoint) == 0 && len(hs.ImageCmd) == 0 {
		return nil // image has no entrypoint (e.g. alpine)
	}

	// Build the command: entrypoint + cmd, or just cmd
	cmd := make([]string, 0, len(hs.ImageEntrypoint)+len(hs.ImageCmd))
	cmd = append(cmd, hs.ImageEntrypoint...)
	cmd = append(cmd, hs.ImageCmd...)

	c, err := s.Connection(handle, nil)
	if err != nil || c == nil {
		return fmt.Errorf("no connection to start image entry: %w", err)
	}

	// Run as background process so it doesn't block the executor.
	background, ok := c.(substrate.ConnectionBackground)
	if !ok {
		return substrate.ErrNotSupported
	}
	_, err = background.ExecBackground(ctx, commandRequest(cmd))
	return err
}

func commandRequest(command []string) substrate.ExecRequest {
	request := substrate.ExecRequest{Shell: substrate.ShellNone}
	if len(command) > 0 {
		request.Program = command[0]
		request.Args = append([]string{}, command[1:]...)
	}
	return request
}

// parseMemoryMiB parses a memory size string ("1024", "1024MiB", "2GiB") into
// MiB, mirroring libvirt's parseMiB and firecracker's parseMemoryMiB. Callers
// convert the result to bytes for HostConfig.Memory.
func parseMemoryMiB(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("memory is empty")
	}
	for _, sfx := range []string{"GIB", "GB", "G"} {
		if strings.HasSuffix(s, sfx) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, sfx), 10, 64)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid memory %q", s)
			}
			return n * 1024, nil
		}
	}
	for _, sfx := range []string{"MIB", "MB", "M"} {
		if strings.HasSuffix(s, sfx) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, sfx), 10, 64)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid memory %q", s)
			}
			return n, nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory %q", s)
	}
	return n, nil
}
