package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	for _, link := range cfg.Links {
		if _, err := normalizeAttachmentAliases(node.Address, link.Aliases); err != nil {
			return nil, fmt.Errorf("node %s link %q: %w", node.Address, link.Name, err)
		}
	}
	name, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return nil, err
	}
	// Firecracker currently launches the VMM inside one network namespace;
	// allowing multiple links here can place TAP devices in different
	// namespaces and only fail after resource creation. Reject the ambiguous
	// topology during planning until a unified namespace contract exists.
	if name == "firecracker" && len(cfg.Links) > 1 {
		return nil, fmt.Errorf("node %s: firecracker supports one network namespace per VM; multiple links require a shared namespace", node.Address)
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
	resolvedEnv, err := e.resolveSecretMap(ctx, cfg.Env)
	if err != nil {
		return state.Resource{}, fmt.Errorf("node %s environment: %w", n.Address, err)
	}
	resolvedProviderConfig, err := secret.ResolveAny(ctx, e.resolver(), cfg.ProviderConfig)
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
	desired, _ := desiredPayload(n)
	desiredValue, err := dynamicValue(desired)
	if err != nil {
		return state.Resource{}, fmt.Errorf("encode desired node configuration: %w", err)
	}

	imageAddr, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
	if err != nil {
		return state.Resource{}, err
	}
	imgState := e.state.FindResource(imageAddr)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("image %s not applied yet", imageAddr)
	}
	imgRef := artifactHandleFromState(imgState)
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
			IPPrefixes: []string{link.IP}, Gateway: link.Gateway, Aliases: append([]string(nil), link.Aliases...),
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
	cleanupNode := func(reason string) {
		// Create/start can have succeeded despite an error response. Retain the
		// partial resource for observation; cleanup is a separately authorized
		// action, never an implicit rollback of this create.
		e.logf("[node %s] retaining partial resource for recovery: %s\n", n.Address, reason)
	}
	if err := e.recordSubstep(parentStep, "create_node", map[string]any{
		"resource":  n.Address.String(),
		"substrate": subName,
		"name":      containerName,
		"image":     imgRef.Identity.Source,
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
			cleanupNode("destroy node on start failure")
			return state.Resource{}, fmt.Errorf("start node %s: %w", n.Address.Name, err)
		}
	}

	// Wire all NICs using the shared helper.
	wireResult, err := wireNICsWithHook(ctx, nicDriver, e.state, handle, nicSpecs, n.Address, e.substepHook(parentStep))
	if err != nil {
		cleanupNode("destroy node on wire failure")
		return state.Resource{}, err
	}

	// Populate PrimaryIP from the wiring result.
	handle.Net.PrimaryIP = wireResult.PrimaryIP
	var guestInitDriver driver.GuestNetworkInit
	if len(caps.GuestNetworkInitModes) > 0 {
		guestInitDriver, err = driver.DefaultRegistry.RequireGuestNetworkInit(subName)
		if err != nil {
			cleanupNode("destroy node without guest network init capability")
			return state.Resource{}, err
		}
		if err := e.recordSubstep(parentStep, "prepare_guest_network", map[string]any{
			"resource": n.Address.String(), "substrate": subName, "node_id": handle.ID,
		}, func() error { return guestInitDriver.PrepareGuestNetwork(ctx, handle) }); err != nil {
			cleanupNode("destroy node on guest network preparation failure")
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
	nodeInstance[desiredPayloadKey] = desiredValue.GoValue()
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
			cleanupNode("destroy node on cold-start failure")
			return state.Resource{}, fmt.Errorf("start node %s: %w", n.Address.Name, err)
		}
	}

	if guestInitDriver != nil {
		observation, observeErr := guestInitDriver.ObserveGuestNetwork(ctx, handle)
		if observeErr != nil {
			cleanupNode("destroy node on guest network observation failure")
			return state.Resource{}, fmt.Errorf("observe guest network for %s: %w", n.Address.Name, observeErr)
		}
		if !observation.Converged {
			cleanupNode("destroy node on guest network convergence failure")
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
		if journalErr := e.journalError(); journalErr != nil {
			return state.Resource{}, journalErr
		}
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

	// Guest execution resolves this identity through the durable action store.
	// Keep it as an incomplete candidate until every create phase succeeds.
	if e.activeCreate != nil {
		if err := e.recordStepExternal(ctx, parentStep, n.Address, controlplane.PlanActionCreate); err != nil {
			return state.Resource{}, err
		}
	}

	if err := e.configureNodeRoutes(ctx, parentStep, n.Address, subName, handle, cfg.Routes, e.state.FindResource(n.Address)); err != nil {
		return state.Resource{}, err
	}

	// Run provisioners after node is up and wired.
	if len(cfg.Provisioners) > 0 {
		conn, err := e.connectionForNode(ctx, nodeDriver, handle, cfg.Connections)
		if err != nil {
			cleanupNode("destroy node on connection setup failure")
			return state.Resource{}, fmt.Errorf("connection for node %s: %w", n.Address.Name, err)
		}
		// Block until the chosen connection is reachable (SSH up, vsock
		// agent listening, ...). Transports that need no wait simply don't
		// implement ConnectionWaiter.
		if waiter, ok := conn.(substrate.ConnectionWaiter); ok {
			e.logf("[provisioner] waiting for connection on %s...\n", n.Address.Name)
			if err := waiter.WaitReady(ctx, 60*time.Second); err != nil {
				cleanupNode("destroy node on connection wait failure")
				return state.Resource{}, fmt.Errorf("connection not ready on node %s: %w", n.Address.Name, err)
			}
		}
		if err := e.runProvisioners(ctx, conn, cfg.Provisioners); err != nil {
			// Retain the uncertain node; guest setup must not be blindly replayed.
			cleanupNode("destroy node on provisioner failure")
			return state.Resource{}, fmt.Errorf("provisioner on node %s: %w", n.Address.Name, err)
		}
	}

	// For Docker nodes, launch the image's original CMD/Entrypoint inside
	// the container (we overrode it with "sleep infinity" during CreateNode).
	if err := e.execImageEntry(ctx, handle, subName); err != nil {
		if journalErr := e.journalError(); journalErr != nil {
			return state.Resource{}, journalErr
		}
		return state.Resource{}, fmt.Errorf("image entry on node %s: %w", n.Address.Name, err)
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
		return fmt.Errorf("reconstruct node %s for destruction: %w", r.Address, err)
	}
	var nicDriver driver.NIC
	requests := make([]driver.AttachmentRequest, 0, len(r.Attachments))
	if len(r.Attachments) > 0 {
		nicDriver, err = driver.DefaultRegistry.RequireNIC(r.Driver)
		if err != nil {
			return err
		}
		for _, attachment := range r.Attachments {
			request, err := attachmentRequestFromState(e.state, attachment)
			if err != nil {
				return fmt.Errorf("validate attachment %s before destruction: %w", attachment.Name, err)
			}
			requests = append(requests, request)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Stop remains best-effort because DestroyNode performs the authoritative,
	// idempotent cleanup for both running and already-absent nodes.
	if err := nodeDriver.StopNode(ctx, handle); err != nil {
		e.logf("[destroy] warning: stop node %s: %v\n", r.Address, err)
	}
	for i, attachment := range r.Attachments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := nicDriver.Delete(ctx, handle, requests[i], attachment.DriverState); err != nil && !driver.IsCategory(err, driver.ErrorNotFound) {
			return fmt.Errorf("delete attachment %s: %w", attachment.Name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := nodeDriver.DestroyNode(ctx, handle); err != nil {
		return fmt.Errorf("destroy node %s: %w", r.Address, err)
	}
	e.state.RemoveResource(r.Address)
	return nil
}

// -- provisioners --

// connectionForNode picks the right Connection implementation based on the
// connectionForNode delegates to Substrate.Connection(). The substrate
// inspects NodeHandle.Conn and the optional HCL hints to pick the right
// implementation (docker-exec, vsock-rpc, SSH, ...).
func (e *Executor) connectionForNode(
	ctx context.Context,
	nodeDriver driver.Node,
	handle substrate.NodeHandle,
	conns []config.ConnectionConfig,
) (substrate.Connection, error) {
	hints, err := e.resolveConnectionHints(ctx, conns)
	if err != nil {
		return nil, err
	}
	return nodeDriver.Connection(handle, hints)
}

func (e *Executor) resolveConnectionHints(ctx context.Context, conns []config.ConnectionConfig) ([]substrate.ConnectionHint, error) {
	hints := make([]substrate.ConnectionHint, len(conns))
	for i, c := range conns {
		password, err := secret.ResolveString(ctx, e.resolver(), c.Password)
		if err != nil {
			return nil, err
		}
		privateKey, err := secret.ResolveString(ctx, e.resolver(), c.PrivateKey)
		if err != nil {
			return nil, err
		}
		hints[i] = substrate.ConnectionHint{
			Type:     c.Type,
			Host:     c.Host,
			User:     c.User,
			Password: password, PrivateKey: privateKey,
			KnownHosts: c.KnownHosts, InsecureSkipHostKeyCheck: c.InsecureSkipHostKeyCheck,
		}
	}
	return hints, nil
}

// runProvisioners executes provisioner blocks in order.
func (e *Executor) runProvisioners(ctx context.Context, conn substrate.Connection, provs []config.ProvisionerConfig) error {
	if conn == nil {
		return fmt.Errorf("no connection available for provisioners")
	}
	for _, p := range provs {
		if err := e.journalError(); err != nil {
			return err
		}
		switch p.Type {
		case "exec":
			program, err := secret.ResolveString(ctx, e.resolver(), p.Program)
			if err != nil {
				return err
			}
			args, err := e.resolveSecretStrings(ctx, p.Args)
			if err != nil {
				return err
			}
			environment, err := e.resolveSecretMap(ctx, p.Environment)
			if err != nil {
				return err
			}
			request := substrate.ExecRequest{Program: program, Args: args, Environment: environment, WorkingDir: p.WorkingDir, Shell: substrate.ShellKind(p.Shell)}
			if err := request.Validate(); err != nil {
				return fmt.Errorf("provisioner exec: %w", err)
			}
			if p.Background {
				background, ok := conn.(substrate.ConnectionStarter)
				if !ok {
					return fmt.Errorf("provisioner background exec: %w", substrate.ErrNotSupported)
				}
				handle, err := background.Start(ctx, request)
				if err != nil {
					return fmt.Errorf("provisioner exec (background): %w", err)
				}
				if handle.ID == "" {
					return fmt.Errorf("%w: background execution returned no handle", ErrActionUncertain)
				}
				if err := e.recordSubstep(e.currentResourceStep, "background_execution", map[string]any{"execution_id": handle.ID, "node_id": handle.NodeID, "generation": handle.Generation, "pid": handle.PID, "request_digest": handle.Digest, "boot_id": handle.BootID, "grant_epoch": handle.Epoch}, func() error { return nil }); err != nil {
					return err
				}
				e.logf("[provisioner] background exec started (execution %s, pid %d)\n", handle.ID, handle.PID)
			} else {
				e.logf("[provisioner] exec: %s\n", p.Program)
				result, err := conn.Exec(ctx, request, e.logger, e.logger)
				if err != nil {
					return err
				}
				if !result.ExitCodeKnown {
					return fmt.Errorf("%w: provisioner exit status unknown", ErrActionUncertain)
				}
				if result.ExitCode != 0 {
					return fmt.Errorf("provisioner exec %s: exit code %d", p.Program, result.ExitCode)
				}
			}
		case "file":
			if p.Source == "" || p.Destination == "" {
				return fmt.Errorf("provisioner file: source and destination required")
			}
			src := expandTilde(p.Source)
			if !filepath.IsAbs(src) && e.workspaceDir != "" {
				src = filepath.Join(e.workspaceDir, src)
			}
			e.logf("[provisioner] file: %s → %s\n", src, p.Destination)
			files, ok := conn.(substrate.ConnectionFileCopier)
			if !ok {
				return fmt.Errorf("provisioner file: %w", substrate.ErrNotSupported)
			}
			if err := files.CopyFile(ctx, src, p.Destination); err != nil {
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
	if err := e.journalError(); err != nil {
		return err
	}
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
