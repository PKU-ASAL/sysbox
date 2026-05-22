package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// createRouter provisions a multi-NIC node with IP forwarding enabled.
// Interfaces on NAT (Docker-managed) networks are connected via Docker
// networking; isolated-network interfaces use veth pairs as usual.
// Optional NAT (nat_from -> nat_to) is configured via host-side nsenter.
type RouterResourceProvider struct{}

func init() {
	RegisterResourceProvider(RouterResourceProvider{})
}

func (RouterResourceProvider) Type() string { return "sysbox_router" }

func (RouterResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_router")
}

func (RouterResourceProvider) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	return readNodeLikeResource(ctx, current)
}

func (RouterResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (RouterResourceProvider) Create(ctx context.Context, exec *Executor, n *graph.Node) (state.Resource, error) {
	return exec.createRouterResource(ctx, n)
}

func (p RouterResourceProvider) Update(ctx context.Context, exec *Executor, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, exec, desired)
}

func (RouterResourceProvider) Delete(ctx context.Context, exec *Executor, current state.Resource) error {
	return exec.destroyNodeResource(ctx, current)
}

func (RouterResourceProvider) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (RouterResourceProvider) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []graph.Ref, error) {
	cfg := &config.RouterConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []graph.Ref
	if ref := config.ResolveName(cfg.Image); ref != "" {
		deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
	}
	for _, iface := range cfg.Interfaces {
		if ref := config.ResolveName(iface.Network); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
		}
	}
	return cfg, deps, nil
}

func (e *Executor) createRouterResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.RouterConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("router %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, err
	}

	imageName := config.ResolveName(cfg.Image)
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("image %s not applied yet", imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.ImageID(),
		Repository: imgState.Repository(),
	}

	// Map RouterInterface → NICSpec for the shared wiring loop.
	var nicSpecs []NICSpec
	for _, iface := range cfg.Interfaces {
		nicSpecs = append(nicSpecs, NICSpec{
			Network: config.ResolveName(iface.Network),
			IP:      iface.IP,
			Label:   iface.Name,
		})
	}

	// Pre-scan: find the first NAT network for InitialLinks.
	initialLinks, err := collectNATLinks(e.state, nicSpecs, false)
	if err != nil {
		return state.Resource{}, err
	}

	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:         fmt.Sprintf("sysbox-%s", n.ID.Name),
		Image:        imgRef,
		Sysctls:      map[string]string{"net.ipv4.ip_forward": "1"},
		InitialLinks: initialLinks,
		Labels:       ManagedLabels(e.topology, e.runID, n.ID),
	})
	if err != nil {
		return state.Resource{}, err
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy router on start failure")
		return state.Resource{}, err
	}

	// Wire all NICs using the shared helper (trackLabels=true for routers).
	wireResult, err := wireNICs(ctx, sub, e.state, handle, initialLinks, nicSpecs, true, n.ID.Name)
	if err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy router on wire failure")
		return state.Resource{}, err
	}

	natApplied := false
	if cfg.NatFrom != "" && cfg.NatTo != "" {
		fromIf, ok1 := wireResult.IfaceByName[cfg.NatFrom]
		toIf, ok2 := wireResult.IfaceByName[cfg.NatTo]
		if !ok1 || !ok2 {
			return state.Resource{}, fmt.Errorf("nat_from %q / nat_to %q must reference declared interfaces",
				cfg.NatFrom, cfg.NatTo)
		}
		if err := configureNATViaNsenter(handle.ID, fromIf, toIf); err != nil {
			e.logf("[router %s] warning: NAT setup failed (continuing without NAT): %v\n", n.ID.Name, err)
		} else {
			natApplied = true
		}
	}

	inst := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   wireResult.PrimaryIP,
		"nics":         wireResult.NICs,
		"nat_applied":  natApplied,
	}
	// Persist provider_extra so cold-destroy works for all substrates.
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		inst["provider_extra"] = string(blob)
	}
	// Persist lifecycle flags.
	if lc := cfg.Lifecycle; lc != nil {
		inst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Type:     "sysbox_router",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: inst,
	}, nil
}

// configureNATViaNsenter configures MASQUERADE and FORWARD rules from the
// host side using nsenter(1) to enter the container's network namespace.
// This avoids the need for iptables inside the container (which would
// require internet access to install via apk/apt-get, and DNS often
// doesn't work in fresh Alpine containers on the Docker default bridge).
//
// The host's iptables binary operates on the kernel's netfilter, which is
// per-network-namespace — so running it via nsenter -t <pid> -n targets
// exactly the right namespace.
func configureNATViaNsenter(containerID, fromIf, toIf string) error {
	// Resolve container PID.
	out, err := execCommand("docker", "inspect", containerID, "--format", "{{.State.Pid}}")
	if err != nil {
		return fmt.Errorf("docker inspect %s: %w", containerID, err)
	}
	pid := strings.TrimSpace(string(out))
	if pid == "0" {
		return fmt.Errorf("container %s not running (pid 0)", containerID)
	}

	// Check that nsenter + iptables are available on the host.
	if _, err := execCommand("nsenter", "--version"); err != nil {
		return fmt.Errorf("nsenter not found on host: %w", err)
	}

	cmds := []string{
		fmt.Sprintf("nsenter -t %s -n iptables -t nat -A POSTROUTING -o %s -j MASQUERADE", pid, toIf),
		fmt.Sprintf("nsenter -t %s -n iptables -A FORWARD -i %s -o %s -j ACCEPT", pid, fromIf, toIf),
		fmt.Sprintf("nsenter -t %s -n iptables -A FORWARD -i %s -o %s -m state --state ESTABLISHED,RELATED -j ACCEPT", pid, toIf, fromIf),
	}
	for _, c := range cmds {
		parts := strings.Fields(c)
		out, err := execCommand(parts[0], parts[1:]...)
		if err != nil {
			return fmt.Errorf("cmd %q: %w (%s)", c, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// execCommand is a small wrapper around exec.Command.CombinedOutput for
// running host-side commands. Extracted for testability.
var execCommand = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
