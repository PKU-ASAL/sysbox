package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

type NodeResourceHandler struct{}

func init() {
	RegisterResourceHandler(NodeResourceHandler{})
}

func (NodeResourceHandler) Type() string { return "sysbox_node" }

func (NodeResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_node")
}

func (NodeResourceHandler) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	return readNodeLikeResource(ctx, current)
}

func (NodeResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (NodeResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createNodeResource(ctx, n)
}

func (NodeResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	return pc.destroyNodeResource(ctx, current)
}

func (NodeResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}

func (NodeResourceHandler) RequiredCapabilities(node *graph.Node) ([]CapabilityRequirement, error) {
	if node.Data == nil {
		return nil, nil
	}
	cfg, ok := node.Data.(*config.NodeConfig)
	if !ok {
		return nil, fmt.Errorf("node %s: wrong data type", node.Address)
	}
	name, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return nil, err
	}
	required := []CapabilityRequirement{{name, driver.CapabilityNode}, {name, driver.CapabilityNIC}, {name, driver.CapabilityNodeState}}
	if len(cfg.Routes) > 0 {
		required = append(required, CapabilityRequirement{name, driver.CapabilityGuestNetwork})
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(name)
	if err != nil {
		return nil, err
	}
	if len(nodeDriver.Capabilities().GuestNetworkInitModes) > 0 {
		required = append(required, CapabilityRequirement{name, driver.CapabilityGuestNetworkInit})
	}
	return required, nil
}

func (NodeResourceHandler) Import(ctx context.Context, addr address.Address, driverName, externalID string) (state.Resource, error) {
	importDriver, err := driver.DefaultRegistry.RequireImport(driverName)
	if err != nil {
		return state.Resource{}, err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(driverName)
	if err != nil {
		return state.Resource{}, err
	}
	handle, err := importDriver.ReadNode(ctx, externalID)
	if err != nil {
		return state.Resource{}, err
	}
	resource := state.Resource{Address: addr, Driver: driverName, ExternalID: handle.ID, Attributes: state.MustAttributes(substrate.HandlePublicAttributes(handle))}
	if blob, err := stateDriver.MarshalProviderState(handle); err != nil {
		return state.Resource{}, err
	} else if len(blob) > 0 {
		if err := resource.SetProviderState(blob); err != nil {
			return state.Resource{}, err
		}
	}
	return resource, nil
}

func (NodeResourceHandler) DecodeResource(r config.ResourceBlock, name string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.NodeConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	if err := decodeNodeProviderConfig(cfg, ctx); err != nil {
		return nil, nil, fmt.Errorf("resource sysbox_node.%s: %w", name, err)
	}
	var deps []address.Address
	if cfg.Image != "" {
		ref, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ref)
	}
	for _, link := range cfg.Links {
		if link.Network != "" {
			ref, err := config.ResolveResourceAddress(link.Network, "sysbox_network")
			if err != nil {
				return nil, nil, err
			}
			deps = append(deps, ref)
		}
	}
	if subName, err := config.ResolveSubstrateRef(cfg.Substrate); err == nil {
		if nodeDriver, err := driver.DefaultRegistry.RequireNode(subName); err == nil {
			pd := nodeDriver.Dependencies(cfg.ProviderConfig)
			for _, ref := range pd.Kernels {
				addr, err := config.ResolveResourceAddress(ref, "sysbox_kernel")
				if err != nil {
					return nil, nil, err
				}
				deps = append(deps, addr)
			}
			for _, ref := range pd.Images {
				addr, err := config.ResolveResourceAddress(ref, "sysbox_image")
				if err != nil {
					return nil, nil, err
				}
				deps = append(deps, addr)
			}
			for _, ref := range pd.Networks {
				addr, err := config.ResolveResourceAddress(ref, "sysbox_network")
				if err != nil {
					return nil, nil, err
				}
				deps = append(deps, addr)
			}
		}
	}
	deps, err := decodeDependsOn(deps, cfg.DependsOn)
	return cfg, deps, err
}

func (DataNodeResourceHandler) DecodeData(d config.DataBlock, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.DataNodeConfig{}
	if err := decodeDataBody(d.Remain, ctx, cfg, "sysbox_node", d.Name); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if ref := config.ResolveName(cfg.Substrate); ref != "" {
		deps = append(deps, address.Address{Type: "substrate", Name: ref})
	}
	return cfg, deps, nil
}

func (e *Executor) createNodeResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.NodeConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("node %s: wrong data type", n.Address)
	}
	resolvedEnv, err := resolveSecretMap(ctx, cfg.Env)
	if err != nil {
		return state.Resource{}, fmt.Errorf("node %s environment: %w", n.Address, err)
	}
	resolvedProviderConfig, err := secret.ResolveAny(ctx, executionSecretResolver, cfg.ProviderConfig)
	if err != nil {
		return state.Resource{}, fmt.Errorf("node %s provider config: %w", n.Address, err)
	}
	providerConfig := resolvedProviderConfig
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(subName)
	if err != nil {
		return state.Resource{}, err
	}
	nicDriver, err := driver.DefaultRegistry.RequireNIC(subName)
	if err != nil {
		return state.Resource{}, err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(subName)
	if err != nil {
		return state.Resource{}, err
	}
	portSpecs, err := normalizePortSpecs(cfg.Ports)
	if err != nil {
		return state.Resource{}, fmt.Errorf("node %s: %w", n.Address.Name, err)
	}
	if err := validatePortExposures(n.Address.Name, subName, nodeDriver, portSpecs); err != nil {
		return state.Resource{}, err
	}
	nodeDesiredHash, err := desiredHash(n)
	if err != nil {
		return state.Resource{}, err
	}

	imageAddr, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
	if err != nil {
		return state.Resource{}, err
	}
	imgState := e.state.FindResource(imageAddr)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("image %s not applied yet", imageAddr)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.ImageID(),
		Repository: imgState.Repository(),
	}
	guestFamily, err := resolveGuestFamily(substrate.GuestFamily(imgState.Str("guest_family")), substrate.GuestFamily(cfg.GuestFamily))
	if err != nil {
		return state.Resource{}, fmt.Errorf("node %s: %w", n.Address.Name, err)
	}

	parentStep := e.currentResourceStep

	// Resolve cross-resource refs (e.g. kernel ref → local path) before CreateNode.
	// We do an early PrepareHandle pass with an empty handle (no PrimaryIP yet)
	// purely for ref resolution. ConnInfo is populated in the second pass below.
	if err := e.recordSubstep(parentStep, "prepare_node_config", map[string]any{
		"resource":  n.Address.String(),
		"substrate": subName,
	}, func() error {
		return nodeDriver.PrepareHandle(ctx, &substrate.NodeHandle{}, providerConfig, stateAdapter{e.state})
	}); err != nil {
		return state.Resource{}, err
	}

	inputs := make([]AttachmentInput, 0, len(cfg.Links))
	for _, link := range cfg.Links {
		inputs = append(inputs, AttachmentInput{
			Name: link.Name, Network: link.Network, MAC: link.MAC,
			IPPrefixes: []string{link.IP}, Gateway: link.Gateway,
		})
	}
	intents, err := NormalizeAttachmentIntents(e.topology, n.Address, inputs)
	if err != nil {
		return state.Resource{}, err
	}
	nicSpecs := nicSpecsFromAttachmentIntents(intents)
	hasNAT := false
	for _, spec := range nicSpecs {
		netAddr, resolveErr := config.ResolveResourceAddress(spec.Network, "sysbox_network")
		if resolveErr != nil {
			return state.Resource{}, resolveErr
		}
		if network := e.state.FindResource(netAddr); network != nil && network.IsNAT() {
			hasNAT = true
			break
		}
	}
	if hasHostPort(portSpecs) {
		if !hasNAT {
			return state.Resource{}, fmt.Errorf("node %s: host port exposure requires at least one nat=true network attachment", n.Address)
		}
	}

	containerName := runtimeExternalName(e.topology, "node", n.Address.Name)
	nodeSpec := substrate.NodeSpec{
		Name:           containerName,
		Image:          imgRef,
		VCPUs:          cfg.Vcpus,
		Memory:         cfg.Memory,
		Env:            resolvedEnv,
		Labels:         ManagedLabels(e.topology, e.runID, n.Address),
		Ports:          portSpecs,
		ManagedNetwork: hasNAT,
		ProviderConfig: providerConfig,
	}
	if err := nodeDriver.Validate(nodeSpec); err != nil {
		return state.Resource{}, err
	}

	var handle substrate.NodeHandle
	if err := e.recordSubstep(parentStep, "create_node", map[string]any{
		"resource":  n.Address.String(),
		"substrate": subName,
		"name":      containerName,
		"image":     imgRef.Repository,
	}, func() error {
		var err error
		handle, err = nodeDriver.CreateNode(ctx, nodeSpec)
		return err
	}); err != nil {
		return state.Resource{}, err
	}

	// Start-node ordering is driven by the substrate's capabilities:
	//   NICHotPlug=true  (docker):  start first, then AttachNIC injects
	//                   veths into the running container's netns.
	//   NICHotPlug=false (FC/VM):  attach NICs first (they must be in the
	//                   boot config), then start the VM.
	caps := nodeDriver.Capabilities()
	if caps.NICHotPlug {
		if err := e.recordSubstep(parentStep, "start_node", map[string]any{
			"resource":  n.Address.String(),
			"substrate": subName,
			"node_id":   handle.ID,
		}, func() error {
			return nodeDriver.StartNode(ctx, handle)
		}); err != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node on start failure")
			return state.Resource{}, fmt.Errorf("start node %s: %w", n.Address.Name, err)
		}
	}

	// Wire all NICs using the shared helper.
	wireResult, err := wireNICsWithHook(ctx, nicDriver, e.state, handle, nicSpecs, n.Address, e.substepHook(parentStep))
	if err != nil {
		util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node on wire failure")
		return state.Resource{}, err
	}

	// Populate PrimaryIP from the wiring result.
	handle.Net.PrimaryIP = wireResult.PrimaryIP
	var guestInitDriver driver.GuestNetworkInit
	if len(caps.GuestNetworkInitModes) > 0 {
		guestInitDriver, err = driver.DefaultRegistry.RequireGuestNetworkInit(subName)
		if err != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node without guest network init capability")
			return state.Resource{}, err
		}
		if err := e.recordSubstep(parentStep, "prepare_guest_network", map[string]any{
			"resource": n.Address.String(), "substrate": subName, "node_id": handle.ID,
		}, func() error { return guestInitDriver.PrepareGuestNetwork(ctx, handle) }); err != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node on guest network preparation failure")
			return state.Resource{}, fmt.Errorf("prepare guest network for %s: %w", n.Address.Name, err)
		}
	}

	resolvedPorts := resolvePorts(portSpecs, handle.Net.PrimaryIP)
	nodeInstance := map[string]any{
		"container_id": handle.ID,
		"primary_ip":   handle.Net.PrimaryIP,
		"ports":        resolvedPorts,
		"guest_family": string(guestFamily),
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
	providerState, _ := stateDriver.MarshalProviderState(handle)
	nodeInstance[desiredHashKey] = nodeDesiredHash
	resource := state.Resource{
		Address:     n.Address,
		Driver:      subName,
		Attributes:  state.MustAttributes(nodeInstance),
		Attachments: wireResult.Attachments,
	}
	if len(providerState) > 0 {
		_ = resource.SetProviderState(providerState)
	}
	e.state.AddResource(resource)
	defer e.state.RemoveResource(resource.Address)

	// Cold-plug substrates (NICHotPlug=false) start the node AFTER all NICs
	// are attached (NICs must be in the boot config). Hot-plug substrates
	// were already started before the NIC loop.
	if !caps.NICHotPlug {
		if err := e.recordSubstep(parentStep, "start_node", map[string]any{
			"resource":  n.Address.String(),
			"substrate": subName,
			"node_id":   handle.ID,
		}, func() error {
			return nodeDriver.StartNode(ctx, handle)
		}); err != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node on cold-start failure")
			return state.Resource{}, fmt.Errorf("start node %s: %w", n.Address.Name, err)
		}
	}

	if guestInitDriver != nil {
		observation, observeErr := guestInitDriver.ObserveGuestNetwork(ctx, handle)
		if observeErr != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node on guest network observation failure")
			return state.Resource{}, fmt.Errorf("observe guest network for %s: %w", n.Address.Name, observeErr)
		}
		if !observation.Converged {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy node on guest network convergence failure")
			return state.Resource{}, fmt.Errorf("guest network for %s did not converge: %s", n.Address.Name, observation.Reason)
		}
		observationState, marshalErr := guestNetworkObservationState(observation)
		if marshalErr != nil {
			return state.Resource{}, marshalErr
		}
		for key, value := range map[string]any{
			"guest_network_init_mode":        string(observation.Mode),
			"guest_network_init_converged":   observation.Converged,
			"guest_network_init_observation": observationState,
		} {
			if setErr := resource.SetRuntimeValue(key, value); setErr != nil {
				return state.Resource{}, setErr
			}
			if record := e.state.FindResource(resource.Address); record != nil {
				if setErr := record.SetRuntimeValue(key, value); setErr != nil {
					return state.Resource{}, setErr
				}
			}
		}
	}

	// Let the substrate populate ConnInfo (Kind, Endpoint, Auth) now that
	// PrimaryIP is set. Each substrate decides what makes sense:
	// docker → ConnKindDocker (set at CreateNode), FC → vsock or SSH.
	if err := e.recordSubstep(parentStep, "prepare_connection", map[string]any{
		"resource":  n.Address.String(),
		"substrate": subName,
		"node_id":   handle.ID,
	}, func() error {
		return nodeDriver.PrepareHandle(ctx, &handle, providerConfig, stateAdapter{e.state})
	}); err != nil {
		e.logf("[apply] warning: PrepareHandle for %s: %v\n", n.Address.Name, err)
	}

	// Re-marshal provider state (the substrate may have mutated HandleState
	// during AttachNIC or PrepareHandle). Always try; substrates with no
	// provider state return (nil, nil) which is harmless.
	if blob, err := stateDriver.MarshalProviderState(handle); err == nil && len(blob) > 0 {
		if rec := e.state.FindResource(address.Resource("sysbox_node", n.Address.Name)); rec != nil {
			_ = rec.SetProviderState(blob)
		}
	}

	// Configure static routes declared in HCL (before provisioners so they
	// can use the routes).
	if len(cfg.Routes) > 0 {
		guestNetwork, err := driver.DefaultRegistry.RequireGuestNetwork(subName)
		if err != nil {
			return state.Resource{}, err
		}
		for _, rt := range cfg.Routes {
			e.logf("[route] %s: %s via %s\n", n.Address.Name, rt.Destination, rt.Via)
			if err := e.recordSubstep(parentStep, "attach_route", map[string]any{
				"resource": n.Address.String(),
				"dst":      rt.Destination,
				"via":      rt.Via,
			}, func() error {
				return guestNetwork.EnsureRoute(ctx, handle, rt.Destination, rt.Via)
			}); err != nil {
				// Non-fatal: route may already exist or ip not available.
				e.logf("[route] warning: %s: %v\n", n.Address.Name, err)
			}
		}
		// Persist routes in state for drift detection.
		routeSpecs := make([]map[string]string, 0, len(cfg.Routes))
		for _, rt := range cfg.Routes {
			routeSpecs = append(routeSpecs, map[string]string{"dst": rt.Destination, "via": rt.Via})
		}
		if rec := e.state.FindResource(address.Resource("sysbox_node", n.Address.Name)); rec != nil {
			_ = rec.SetAttribute("routes", routeSpecs)
		}
	}

	// Run provisioners after node is up and wired.
	if len(cfg.Provisioners) > 0 {
		conn, err := connectionForNode(ctx, nodeDriver, handle, cfg.Connections)
		if err != nil {
			return state.Resource{}, fmt.Errorf("connection for node %s: %w", n.Address.Name, err)
		}
		// Block until the chosen connection is reachable (SSH up, vsock
		// agent listening, ...). Transports that need no wait simply don't
		// implement ConnectionWaiter.
		if waiter, ok := conn.(substrate.ConnectionWaiter); ok {
			e.logf("[provisioner] waiting for connection on %s...\n", n.Address.Name)
			if err := waiter.WaitReady(ctx, 60*time.Second); err != nil {
				return state.Resource{}, fmt.Errorf("connection not ready on node %s: %w", n.Address.Name, err)
			}
		}
		if err := e.runProvisioners(ctx, conn, cfg.Provisioners); err != nil {
			return state.Resource{}, fmt.Errorf("provisioner on node %s: %w", n.Address.Name, err)
		}
	}

	// For Docker nodes, launch the image's original CMD/Entrypoint inside
	// the container (we overrode it with "sleep infinity" during CreateNode).
	if err := e.execImageEntry(ctx, handle, subName); err != nil {
		e.logf("[node] warning: image entry start: %v\n", err)
	}

	if rec := e.state.FindResource(address.Resource("sysbox_node", n.Address.Name)); rec != nil {
		resource = *rec
	}
	return resource, nil
}

func guestNetworkObservationState(observation substrate.GuestNetworkInitObservation) (map[string]any, error) {
	raw, err := json.Marshal(observation)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func hasHostPort(ports []substrate.PortSpec) bool {
	for _, port := range ports {
		if port.Exposure == substrate.PortExposureHost {
			return true
		}
	}
	return false
}

func (e *Executor) destroyNodeResource(ctx context.Context, r state.Resource) error {
	nodeDriver, err := driver.DefaultRegistry.RequireNode(r.Driver)
	if err != nil {
		return err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(r.Driver)
	if err != nil {
		return err
	}
	handle, err := r.ReconstructHandle(stateDriver)
	if err != nil {
		e.logf("[destroy] warning: reconstruct node %s: %v\n", r.Address, err)
		handle = substrate.NodeHandle{ID: r.ContainerID()}
	}
	// Ignore stop/destroy errors: container may already be gone (drift recovery).
	if err := nodeDriver.StopNode(ctx, handle); err != nil {
		e.logf("[destroy] warning: stop node %s: %v\n", r.Address, err)
	}
	if nicDriver, nicErr := driver.DefaultRegistry.RequireNIC(r.Driver); nicErr == nil {
		for _, attachment := range r.Attachments {
			request, reqErr := attachmentRequestFromState(e.state, attachment)
			if reqErr != nil {
				e.logf("[destroy] warning: attachment %s: %v\n", attachment.Name, reqErr)
				continue
			}
			if err := nicDriver.Delete(ctx, handle, request, attachment.DriverState); err != nil && !driver.IsCategory(err, driver.ErrorNotFound) {
				e.logf("[destroy] warning: delete attachment %s: %v\n", attachment.Name, err)
			}
		}
	}
	if err := nodeDriver.DestroyNode(ctx, handle); err != nil {
		e.logf("[destroy] warning: destroy node %s: %v\n", r.Address, err)
	}
	e.state.RemoveResource(r.Address)
	return nil
}

// -- provisioners --

// connectionForNode picks the right Connection implementation based on the
// connectionForNode delegates to Substrate.Connection(). The substrate
// inspects NodeHandle.Conn and the optional HCL hints to pick the right
// implementation (docker-exec, vsock-rpc, SSH, ...).
func connectionForNode(
	ctx context.Context,
	nodeDriver driver.Node,
	handle substrate.NodeHandle,
	conns []config.ConnectionConfig,
) (substrate.Connection, error) {
	hints := make([]substrate.ConnectionHint, len(conns))
	for i, c := range conns {
		password, err := secret.ResolveString(ctx, executionSecretResolver, c.Password)
		if err != nil {
			return nil, err
		}
		privateKey, err := secret.ResolveString(ctx, executionSecretResolver, c.PrivateKey)
		if err != nil {
			return nil, err
		}
		hints[i] = substrate.ConnectionHint{
			Type:     c.Type,
			Host:     c.Host,
			User:     c.User,
			Password: password, PrivateKey: privateKey,
		}
	}
	return nodeDriver.Connection(handle, hints)
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
			resolvedInline, err := resolveSecretStrings(ctx, p.Inline)
			if err != nil {
				return err
			}
			if p.Background {
				cmd := []string{"sh", "-c", strings.Join(resolvedInline, " && ")}
				pid, err := conn.ExecBackground(ctx, cmd, nil)
				if err != nil {
					return fmt.Errorf("provisioner exec (background): %w", err)
				}
				e.logf("[provisioner] background exec started (pid %d)\n", pid)
			} else {
				e.logf("[provisioner] exec: %d command(s)\n", len(resolvedInline))
				if err := conn.ExecInline(ctx, resolvedInline); err != nil {
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

// execImageEntry launches the image's original CMD/Entrypoint on substrates
// that override it at create time (probed via substrate.ImageEntryStarter,
// currently only docker).
func (e *Executor) execImageEntry(ctx context.Context, handle substrate.NodeHandle, subName string) error {
	descriptor, ok := driver.DefaultRegistry.Get(subName)
	if !ok {
		return driver.Wrap(driver.ErrorNotFound, subName, "driver is not registered", nil)
	}
	if descriptor.ImageEntry == nil {
		return nil // substrate runs the image entry natively
	}
	e.logf("[node] starting image entry on %s\n", handle.ID)
	return descriptor.ImageEntry.ExecImageEntry(ctx, handle)
}
