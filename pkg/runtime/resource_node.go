package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	providerexec "github.com/oslab/sysbox/pkg/provider/exec"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

type NodeResourceProvider struct{}

func init() {
	RegisterResourceProvider(NodeResourceProvider{})
}

func (NodeResourceProvider) Type() string { return "sysbox_node" }

func (NodeResourceProvider) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_node")
}

func (NodeResourceProvider) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	return readNodeLikeResource(ctx, current)
}

func (NodeResourceProvider) PlanDiff(desired *graph.Node, current *state.Resource) (PlanAction, error) {
	return planDiffByDesiredHash(desired, current)
}

func (NodeResourceProvider) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createNodeResource(ctx, n)
}

func (p NodeResourceProvider) Update(ctx context.Context, pc *ProviderContext, desired *graph.Node, _ state.Resource) (state.Resource, error) {
	return p.Create(ctx, pc, desired)
}

func (NodeResourceProvider) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	return pc.destroyNodeResource(ctx, current)
}

func (NodeResourceProvider) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (NodeResourceProvider) DecodeResource(r config.ResourceBlock, name string, ctx *hcl.EvalContext) (any, []graph.Ref, error) {
	cfg := &config.NodeConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	if err := decodeNodeProviderConfig(cfg, ctx); err != nil {
		return nil, nil, fmt.Errorf("resource sysbox_node.%s: %w", name, err)
	}
	var deps []graph.Ref
	if ref := config.ResolveName(cfg.Image); ref != "" {
		deps = append(deps, graph.Ref{Type: "sysbox_image", Name: ref})
	}
	for _, link := range cfg.Links {
		if ref := config.ResolveName(link.Network); ref != "" {
			deps = append(deps, graph.Ref{Type: "sysbox_network", Name: ref})
		}
	}
	if subName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
		if sub, err := substrate.Get(subName); err == nil {
			pd := sub.Dependencies(cfg.ProviderConfig)
			for _, n := range pd.Kernels {
				deps = append(deps, graph.Ref{Type: "sysbox_kernel", Name: n})
			}
			for _, n := range pd.Images {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: n})
			}
			for _, n := range pd.Networks {
				deps = append(deps, graph.Ref{Type: "sysbox_network", Name: n})
			}
		}
	}
	deps = decodeDependsOn(deps, cfg.DependsOn)
	return cfg, deps, nil
}

func (DataNodeResourceProvider) DecodeData(d config.DataBlock, ctx *hcl.EvalContext) (any, []graph.Ref, error) {
	cfg := &config.DataNodeConfig{}
	if err := decodeDataBody(d.Remain, ctx, cfg, "sysbox_node", d.Name); err != nil {
		return nil, nil, err
	}
	var deps []graph.Ref
	if ref := config.ResolveName(cfg.Substrate); ref != "" {
		deps = append(deps, graph.Ref{Type: "substrate", Name: ref})
	}
	return cfg, deps, nil
}

func (e *Executor) createNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.NodeConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("node %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return state.Resource{}, err
	}
	nodeDesiredHash, err := desiredHash(n)
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

	parentStep := e.currentResourceStep

	// Resolve cross-resource refs (e.g. kernel ref → local path) before CreateNode.
	// We do an early PrepareHandle pass with an empty handle (no PrimaryIP yet)
	// purely for ref resolution. ConnInfo is populated in the second pass below.
	if err := e.recordSubstep(parentStep, "prepare_node_config", map[string]any{
		"resource":  n.ID.String(),
		"substrate": subName,
	}, func() error {
		return sub.PrepareHandle(ctx, &substrate.NodeHandle{}, cfg.ProviderConfig, stateAdapter{e.state})
	}); err != nil {
		return state.Resource{}, err
	}

	// Map LinkConfig → NICSpec for the shared wiring loop.
	var nicSpecs []NICSpec
	for _, link := range cfg.Links {
		nicSpecs = append(nicSpecs, NICSpec{
			Network: config.ResolveName(link.Network),
			IP:      link.IP,
			Gateway: link.Gateway,
		})
	}

	// Pre-scan: find Docker NAT networks for InitialLinks.
	initialLinks, err := collectNATLinks(e.state, nicSpecs, true)
	if err != nil {
		return state.Resource{}, err
	}

	var handle substrate.NodeHandle
	if err := e.recordSubstep(parentStep, "create_node", map[string]any{
		"resource":  n.ID.String(),
		"substrate": subName,
		"name":      fmt.Sprintf("sysbox-%s", n.ID.Name),
		"image":     imgRef.Repository,
	}, func() error {
		var err error
		handle, err = sub.CreateNode(ctx, substrate.NodeSpec{
			Name:           fmt.Sprintf("sysbox-%s", n.ID.Name),
			Image:          imgRef,
			VCPUs:          cfg.Vcpus,
			Memory:         cfg.Memory,
			Env:            cfg.Env,
			Labels:         ManagedLabels(e.topology, e.runID, n.ID),
			InitialLinks:   initialLinks,
			ProviderConfig: cfg.ProviderConfig,
		})
		return err
	}); err != nil {
		return state.Resource{}, err
	}

	// Start-node ordering is driven by the substrate's capabilities:
	//   NICHotPlug=true  (docker):  start first, then AttachNIC injects
	//                   veths into the running container's netns.
	//   NICHotPlug=false (FC/VM):  attach NICs first (they must be in the
	//                   boot config), then start the VM.
	caps := sub.Capabilities()
	if caps.NICHotPlug {
		if err := e.recordSubstep(parentStep, "start_node", map[string]any{
			"resource":  n.ID.String(),
			"substrate": subName,
			"node_id":   handle.ID,
		}, func() error {
			return sub.StartNode(ctx, handle)
		}); err != nil {
			util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy node on start failure")
			return state.Resource{}, fmt.Errorf("start node %s: %w", n.ID.Name, err)
		}
	}

	// Wire all NICs using the shared helper.
	wireResult, err := wireNICsWithHook(ctx, sub, e.state, handle, initialLinks, nicSpecs, false, n.ID.Name, e.substepHook(parentStep))
	if err != nil {
		util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy node on wire failure")
		return state.Resource{}, err
	}

	// Populate PrimaryIP from the wiring result.
	handle.Net.PrimaryIP = wireResult.PrimaryIP

	nodeInstance := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
		"nics":         wireResult.NICs,
	}
	// Persist lifecycle flags so ComputePlan can honour them on future runs
	// even if the resource is removed from HCL.
	if lc := cfg.Lifecycle; lc != nil {
		nodeInstance["lifecycle_prevent_destroy"] = lc.PreventDestroy
		if len(lc.IgnoreChanges) > 0 {
			nodeInstance["lifecycle_ignore_changes"] = lc.IgnoreChanges
		}
	}
	// Substrate-specific state (vsock metadata, vm_dir, etc.) goes through
	// MarshalProviderState so runtime stays substrate-agnostic.
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		nodeInstance["provider_extra"] = string(blob)
	}
	nodeInstance[desiredHashKey] = nodeDesiredHash
	resource := state.Resource{
		Type:     "sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: nodeInstance,
	}
	e.state.AddResource(resource)
	defer e.state.RemoveResource(resource.Type, resource.Name)

	// Cold-plug substrates (NICHotPlug=false) start the node AFTER all NICs
	// are attached (NICs must be in the boot config). Hot-plug substrates
	// were already started before the NIC loop.
	if !caps.NICHotPlug {
		if err := e.recordSubstep(parentStep, "start_node", map[string]any{
			"resource":  n.ID.String(),
			"substrate": subName,
			"node_id":   handle.ID,
		}, func() error {
			return sub.StartNode(ctx, handle)
		}); err != nil {
			util.BestEffortIgnore(func() error { return sub.DestroyNode(ctx, handle) }, "destroy node on cold-start failure")
			return state.Resource{}, fmt.Errorf("start node %s: %w", n.ID.Name, err)
		}
	}

	// Let the substrate populate ConnInfo (Kind, Endpoint, Auth) now that
	// PrimaryIP is set. Each substrate decides what makes sense:
	// docker → ConnKindDocker (set at CreateNode), FC → vsock or SSH.
	if err := e.recordSubstep(parentStep, "prepare_connection", map[string]any{
		"resource":  n.ID.String(),
		"substrate": subName,
		"node_id":   handle.ID,
	}, func() error {
		return sub.PrepareHandle(ctx, &handle, cfg.ProviderConfig, stateAdapter{e.state})
	}); err != nil {
		e.logf("[apply] warning: PrepareHandle for %s: %v\n", n.ID.Name, err)
	}

	// Re-marshal provider state (the substrate may have mutated HandleState
	// during AttachNIC or PrepareHandle). Always try; substrates with no
	// provider state return (nil, nil) which is harmless.
	if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		if rec := e.state.FindResource("sysbox_node", n.ID.Name); rec != nil {
			rec.Instance["provider_extra"] = string(blob)
		}
	}

	// Configure static routes declared in HCL (before provisioners so they
	// can use the routes). This replaces `ip route add` in provisioners.
	if len(cfg.Routes) > 0 {
		conn, err := connectionForNode(sub, handle, cfg.Connections)
		if err != nil {
			return state.Resource{}, fmt.Errorf("connection for routes on node %s: %w", n.ID.Name, err)
		}
		for _, rt := range cfg.Routes {
			cmd := fmt.Sprintf("ip route replace %s via %s", rt.Destination, rt.Via)
			e.logf("[route] %s: %s\n", n.ID.Name, cmd)
			if err := e.recordSubstep(parentStep, "attach_route", map[string]any{
				"resource": n.ID.String(),
				"dst":      rt.Destination,
				"via":      rt.Via,
			}, func() error {
				return conn.ExecInline(ctx, []string{cmd})
			}); err != nil {
				// Non-fatal: route may already exist or ip not available.
				e.logf("[route] warning: %s: %v\n", n.ID.Name, err)
			}
		}
		// Persist routes in state for drift detection.
		routeSpecs := make([]map[string]string, 0, len(cfg.Routes))
		for _, rt := range cfg.Routes {
			routeSpecs = append(routeSpecs, map[string]string{"dst": rt.Destination, "via": rt.Via})
		}
		if rec := e.state.FindResource("sysbox_node", n.ID.Name); rec != nil {
			rec.Instance["routes"] = routeSpecs
		}
	}

	// Run provisioners after node is up and wired.
	if len(cfg.Provisioners) > 0 {
		conn, err := connectionForNode(sub, handle, cfg.Connections)
		if err != nil {
			return state.Resource{}, fmt.Errorf("connection for node %s: %w", n.ID.Name, err)
		}
		// Block until the chosen connection is reachable.
		switch c := conn.(type) {
		case *providerexec.SSHConnection:
			if c != nil {
				e.logf("[provisioner] waiting for SSH on %s...\n", c.Host())
				if err := c.WaitForSSH(ctx, 60*time.Second); err != nil {
					return state.Resource{}, fmt.Errorf("ssh not ready on node %s: %w", n.ID.Name, err)
				}
			}
		case *providerexec.VsockConnection:
			if c != nil {
				e.logf("[provisioner] waiting for vsock-agent on %s...\n", n.ID.Name)
				if err := c.WaitReady(ctx, 60*time.Second); err != nil {
					return state.Resource{}, fmt.Errorf("vsock-agent not ready on node %s: %w", n.ID.Name, err)
				}
			}
		}
		if err := e.runProvisioners(ctx, conn, cfg.Provisioners); err != nil {
			return state.Resource{}, fmt.Errorf("provisioner on node %s: %w", n.ID.Name, err)
		}
	}

	// For Docker nodes, launch the image's original CMD/Entrypoint inside
	// the container (we overrode it with "sleep infinity" during CreateNode).
	if err := e.execImageEntry(ctx, handle, subName); err != nil {
		e.logf("[node] warning: image entry start: %v\n", err)
	}

	if rec := e.state.FindResource("sysbox_node", n.ID.Name); rec != nil {
		resource = *rec
	}
	return resource, nil
}

func (e *Executor) destroyNodeResource(ctx context.Context, r state.Resource) error {
	sub, err := substrate.Get(r.Provider)
	if err != nil {
		return err
	}
	handle, err := r.ReconstructHandle(sub)
	if err != nil {
		e.logf("[destroy] warning: reconstruct node %s: %v\n", r.Name, err)
		handle = substrate.NodeHandle{ID: r.ContainerID()}
	}
	// Ignore stop/destroy errors: container may already be gone (drift recovery).
	if err := sub.StopNode(ctx, handle); err != nil {
		e.logf("[destroy] warning: stop node %s: %v\n", r.Name, err)
	}
	if err := sub.DestroyNode(ctx, handle); err != nil {
		e.logf("[destroy] warning: destroy node %s: %v\n", r.Name, err)
	}
	// Always clean up veths/taps and state regardless of container presence.
	if nics, ok := r.Instance["nics"].([]any); ok {
		for _, item := range nics {
			n, _ := item.(map[string]any)
			kind := util.AsString(n["kind"])
			hostEnd := util.AsString(n["host_end"])
			nsName := util.AsString(n["netns"])
			if kind == "tap" {
				if err := network.DeleteTapDevice(hostEnd, nsName); err != nil {
					e.logf("[destroy] warning: delete tap %s: %v\n", hostEnd, err)
				}
			} else {
				if err := network.DeleteVethPair(network.VethHandle{HostEnd: hostEnd, NetnsName: nsName}); err != nil {
					e.logf("[destroy] warning: delete veth %s: %v\n", hostEnd, err)
				}
			}
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- provisioners --

// connectionForNode picks the right Connection implementation based on the
// connectionForNode delegates to Substrate.Connection(). The substrate
// inspects NodeHandle.Conn and the optional HCL hints to pick the right
// implementation (docker-exec, vsock-rpc, SSH, ...).
func connectionForNode(
	sub substrate.Substrate,
	handle substrate.NodeHandle,
	conns []config.ConnectionConfig,
) (substrate.Connection, error) {
	hints := make([]substrate.ConnectionHint, len(conns))
	for i, c := range conns {
		hints[i] = substrate.ConnectionHint{
			Type:       c.Type,
			Host:       c.Host,
			User:       c.User,
			Password:   c.Password,
			PrivateKey: c.PrivateKey,
		}
	}
	return sub.Connection(handle, hints)
}

// runProvisioners executes provisioner blocks in order.
func (e *Executor) runProvisioners(ctx context.Context, conn substrate.Connection, provs []config.ProvisionerConfig) error {
	if conn == nil {
		return fmt.Errorf("no connection available for provisioners")
	}
	for _, p := range provs {
		switch p.Type {
		case "exec":
			if len(p.Inline) == 0 {
				continue
			}
			if p.Background {
				cmd := []string{"sh", "-c", strings.Join(p.Inline, " && ")}
				pid, err := conn.ExecBackground(ctx, cmd, nil)
				if err != nil {
					return fmt.Errorf("provisioner exec (background): %w", err)
				}
				e.logf("[provisioner] background exec started (pid %d)\n", pid)
			} else {
				e.logf("[provisioner] exec: %v\n", p.Inline)
				if err := conn.ExecInline(ctx, p.Inline); err != nil {
					return err
				}
			}
		case "file":
			if p.Source == "" || p.Destination == "" {
				return fmt.Errorf("provisioner file: source and destination required")
			}
			src := expandTilde(p.Source)
			e.logf("[provisioner] file: %s → %s\n", src, p.Destination)
			if err := conn.CopyFile(ctx, src, p.Destination); err != nil {
				return fmt.Errorf("provisioner file %s: %w", src, err)
			}
		default:
			return fmt.Errorf("unknown provisioner type %q", p.Type)
		}
	}
	return nil
}

// execImageEntry launches the image's original CMD/Entrypoint inside a Docker
// container. During CreateNode we override with "sleep infinity" so provisioners
// can run; after provisioning we exec the original entrypoint so services
// (nginx, postgres, etc.) actually start.
func (e *Executor) execImageEntry(ctx context.Context, handle substrate.NodeHandle, subName string) error {
	if subName != "docker" {
		return nil // only Docker overrides the entrypoint
	}
	hs, ok := handle.Provider.(*dockerprovider.HandleState)
	if !ok || hs == nil {
		return nil
	}
	if len(hs.ImageEntrypoint) == 0 && len(hs.ImageCmd) == 0 {
		return nil // image has no entrypoint (e.g. alpine)
	}

	// Build the command: entrypoint + cmd, or just cmd
	cmd := make([]string, 0, len(hs.ImageEntrypoint)+len(hs.ImageCmd))
	cmd = append(cmd, hs.ImageEntrypoint...)
	cmd = append(cmd, hs.ImageCmd...)

	conn, err := substrate.Get(subName)
	if err != nil {
		return err
	}
	dsub := conn.(substrate.Substrate)
	c, err := dsub.Connection(handle, nil)
	if err != nil || c == nil {
		return fmt.Errorf("no connection to start image entry: %w", err)
	}

	e.logf("[node] starting image entry: %v\n", cmd)
	// Run as background process so it doesn't block the executor
	_, err = c.ExecBackground(ctx, cmd, nil)
	return err
}
