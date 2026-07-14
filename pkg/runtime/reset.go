package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/secret"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

const resetReason = "reset mutable guest state from immutable baseline"

func ResumeResetPlan(checkpoint *OperationCheckpoint, current *Plan) (*Plan, error) {
	if checkpoint == nil || current == nil {
		return nil, fmt.Errorf("reset resume requires checkpoint and current plan")
	}
	fingerprint, err := planActionsSHA256(checkpoint.Plan)
	if err != nil {
		return nil, err
	}
	if checkpoint.PlanSHA256 == "" || checkpoint.PlanSHA256 != fingerprint {
		return nil, fmt.Errorf("checkpoint plan fingerprint mismatch")
	}
	currentByAddress := make(map[string]controlplane.PlannedChange, len(current.Actions))
	for _, action := range current.Actions {
		currentByAddress[action.Address.String()] = action
	}
	for _, parentAction := range checkpoint.Plan {
		currentAction, ok := currentByAddress[parentAction.Address.String()]
		if !ok || !reflect.DeepEqual(parentAction, currentAction) {
			return nil, fmt.Errorf("stale reset resume: current pinned plan differs from parent checkpoint for %s", parentAction.Address)
		}
	}
	completed := make(map[string]struct{})
	for _, step := range checkpoint.Steps {
		if step.Kind == "resource" && step.Action == controlplane.PlanActionReset && step.Status == OperationDone {
			completed[step.Resource] = struct{}{}
		}
	}
	resumed := &Plan{}
	for _, action := range checkpoint.Plan {
		if _, ok := completed[action.Address.String()]; !ok {
			resumed.Actions = append(resumed.Actions, action)
		}
	}
	return resumed, resumed.Validate()
}

func BuildResetPlan(topology *graph.Graph, current *state.State, target string) (*Plan, error) {
	if topology == nil || current == nil {
		return nil, fmt.Errorf("reset requires topology and state")
	}
	if err := topology.Validate(); err != nil {
		return nil, fmt.Errorf("validate graph: %w", err)
	}
	var targetAddress address.Address
	if target != "" {
		parsed, err := address.Parse(target)
		if err != nil {
			return nil, fmt.Errorf("invalid reset target %q: %w", target, err)
		}
		if parsed.Type != "sysbox_node" {
			return nil, fmt.Errorf("reset target %s must be a sysbox_node", parsed)
		}
		if topology.Get(parsed) == nil {
			return nil, fmt.Errorf("reset target %s is not declared", parsed)
		}
		if current.FindResource(parsed) == nil {
			return nil, fmt.Errorf("reset target %s is not present in state", parsed)
		}
		targetAddress = parsed
	}
	order, err := topology.TopoSort()
	if err != nil {
		return nil, err
	}
	plan := &Plan{}
	for _, resourceAddress := range order {
		if resourceAddress.Type != "sysbox_node" || current.FindResource(resourceAddress) == nil {
			continue
		}
		if target != "" && !resourceAddress.Equal(targetAddress) {
			continue
		}
		node := topology.Get(resourceAddress)
		cfg, ok := node.Data.(*config.NodeConfig)
		if !ok {
			return nil, fmt.Errorf("reset node %s has invalid configuration", resourceAddress)
		}
		imageAddress, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
		if err != nil {
			return nil, err
		}
		baseline := artifactHandleFromState(current.FindResource(imageAddress))
		if err := baseline.Validate(); err != nil {
			return nil, fmt.Errorf("reset node %s has no immutable baseline: %w", resourceAddress, err)
		}
		plan.Actions = append(plan.Actions, controlplane.PlannedChange{
			Address: resourceAddress, Action: controlplane.PlanActionReset, Reason: resetReason,
			Changes: []controlplane.FieldChange{{Path: "baseline_digest", After: baseline.Identity.Digest}},
		})
	}
	if len(plan.Actions) == 0 {
		return nil, fmt.Errorf("reset plan contains no managed nodes")
	}
	return plan, plan.Validate()
}

type resetNodeContext struct {
	action      controlplane.PlannedChange
	node        *graph.Node
	config      *config.NodeConfig
	current     *state.Resource
	nodeDriver  driver.Node
	nicDriver   driver.NIC
	stateDriver driver.NodeState
	resetDriver driver.Reset
	request     substrate.ResetRequest
	nicSpecs    []NICSpec
	handle      substrate.ResetHandle
	step        int
}

func (e *Executor) Reset(ctx context.Context, plan *Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate reset plan: %w", err)
	}
	contexts := make([]*resetNodeContext, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		if action.Action != controlplane.PlanActionReset {
			return fmt.Errorf("reset plan contains non-reset action %q for %s", action.Action, action.Address)
		}
		prepared, err := e.buildResetNodeContext(ctx, action)
		if err != nil {
			return err
		}
		contexts = append(contexts, prepared)
	}
	if err := e.recorder.Begin("reset", plan); err != nil {
		return err
	}
	var resetErr error
	defer func() { e.recorder.Finish(resetErr) }()

	for i := len(contexts) - 1; i >= 0; i-- {
		item := contexts[i]
		item.step = e.recorder.StepStart(item.action.Address.String(), controlplane.PlanActionReset)
		if err := e.prepareResetNode(ctx, item); err != nil {
			e.recorder.StepFailed(item.step, err)
			resetErr = err
			return err
		}
	}
	for _, item := range contexts {
		beforeApply := cloneResetStateResource(*item.current)
		if err := e.applyResetNode(ctx, item); err != nil {
			e.recorder.StepFailed(item.step, err)
			resetErr = err
			return err
		}
		if err := e.recordStepExternal(ctx, item.step, item.action.Address, controlplane.PlanActionReset); err != nil {
			restoreResetStateResource(e.state, beforeApply)
			item.current = e.state.FindResource(item.action.Address)
			e.recorder.StepFailed(item.step, err)
			resetErr = err
			return err
		}
		item.current = e.state.FindResource(item.action.Address)
		e.recorder.StepDone(item.step)
	}
	return nil
}

func (e *Executor) buildResetNodeContext(ctx context.Context, action controlplane.PlannedChange) (*resetNodeContext, error) {
	node := e.graph.Get(action.Address)
	if node == nil || node.Address.Type != "sysbox_node" {
		return nil, fmt.Errorf("reset node %s is not declared", action.Address)
	}
	cfg, ok := node.Data.(*config.NodeConfig)
	if !ok {
		return nil, fmt.Errorf("reset node %s has invalid configuration", action.Address)
	}
	current := e.state.FindResource(action.Address)
	if current == nil {
		return nil, fmt.Errorf("reset node %s is not present in state", action.Address)
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(current.Driver)
	if err != nil {
		return nil, err
	}
	nicDriver, err := driver.DefaultRegistry.RequireNIC(current.Driver)
	if err != nil {
		return nil, err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(current.Driver)
	if err != nil {
		return nil, err
	}
	resetDriver, err := driver.DefaultRegistry.RequireReset(current.Driver)
	if err != nil {
		return nil, err
	}
	currentHandle, err := current.ReconstructHandle(stateDriver)
	if err != nil {
		return nil, fmt.Errorf("reset node %s: %w", action.Address, err)
	}
	imageAddress, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
	if err != nil {
		return nil, err
	}
	imageState := e.state.FindResource(imageAddress)
	baseline := artifactHandleFromState(imageState)
	if err := baseline.Validate(); err != nil {
		return nil, fmt.Errorf("reset node %s has no immutable baseline: %w", action.Address, err)
	}
	if baseline.Identity.GuestFamily == substrate.GuestFamilyUnknown {
		return nil, fmt.Errorf("reset node %s requires a known guest family", action.Address)
	}
	pinnedDigest := ""
	for _, change := range action.Changes {
		if change.Path == "baseline_digest" {
			pinnedDigest, _ = change.After.(string)
			break
		}
	}
	if pinnedDigest == "" || pinnedDigest != baseline.Identity.Digest {
		return nil, fmt.Errorf("stale reset plan for %s: baseline digest changed from %q to %q", action.Address, pinnedDigest, baseline.Identity.Digest)
	}
	environment, err := resolveSecretMap(ctx, cfg.Env)
	if err != nil {
		return nil, err
	}
	providerConfig, err := secret.ResolveAny(ctx, executionSecretResolver, cfg.ProviderConfig)
	if err != nil {
		return nil, err
	}
	if err := nodeDriver.PrepareHandle(ctx, &substrate.NodeHandle{}, providerConfig, stateAdapter{e.state}); err != nil {
		return nil, err
	}
	ports, err := normalizePortSpecs(cfg.Ports)
	if err != nil {
		return nil, err
	}
	inputs := make([]AttachmentInput, 0, len(cfg.Links))
	for _, link := range cfg.Links {
		inputs = append(inputs, AttachmentInput{Name: link.Name, Network: link.Network, MAC: link.MAC, IPPrefixes: []string{link.IP}, Gateway: link.Gateway})
	}
	intents, err := NormalizeAttachmentIntents(e.topology, node.Address, inputs)
	if err != nil {
		return nil, err
	}
	nicSpecs := nicSpecsFromAttachmentIntents(intents)
	hasNAT := false
	for _, spec := range nicSpecs {
		networkAddress, resolveErr := config.ResolveResourceAddress(spec.Network, "sysbox_network")
		if resolveErr != nil {
			return nil, resolveErr
		}
		if network := e.state.FindResource(networkAddress); network != nil && network.IsNAT() {
			hasNAT = true
		}
	}
	nodeSpec := substrate.NodeSpec{Name: runtimeExternalName(e.topology, "node", node.Address.Name), Image: baseline, VCPUs: cfg.Vcpus, Memory: cfg.Memory, Env: environment, Labels: ManagedLabels(e.topology, e.runID, node.Address), Ports: ports, ManagedNetwork: hasNAT, ProviderConfig: providerConfig}
	if err := nodeDriver.Validate(nodeSpec); err != nil {
		return nil, err
	}
	return &resetNodeContext{action: action, node: node, config: cfg, current: current, nodeDriver: nodeDriver, nicDriver: nicDriver, stateDriver: stateDriver, resetDriver: resetDriver, request: substrate.ResetRequest{Current: currentHandle, Node: nodeSpec, Baseline: baseline.Identity}, nicSpecs: nicSpecs, step: -1}, nil
}

func (e *Executor) prepareResetNode(ctx context.Context, item *resetNodeContext) error {
	if encoded, _ := item.current.RuntimeValue("reset_handle").(string); encoded != "" {
		handle, err := item.resetDriver.UnmarshalResetHandle(json.RawMessage(encoded))
		if err != nil {
			return fmt.Errorf("restore reset handle for %s: %w", item.action.Address, err)
		}
		item.handle = handle
		if err := e.recordSubstep(item.step, "resume_observe_reset", nil, func() error {
			_, err := item.resetDriver.ObserveReset(ctx, handle)
			return err
		}); err != nil {
			return fmt.Errorf("observe reset for %s: %w", item.action.Address, err)
		}
		return nil
	}
	for _, attachment := range item.current.Attachments {
		request, err := attachmentRequestFromState(e.state, attachment)
		if err != nil {
			return err
		}
		err = e.recordSubstep(item.step, "delete_attachment", map[string]any{"name": attachment.Name}, func() error {
			deleteErr := item.nicDriver.Delete(ctx, item.request.Current, request, attachment.DriverState)
			if driver.IsCategory(deleteErr, driver.ErrorNotFound) {
				return nil
			}
			return deleteErr
		})
		if err != nil {
			return fmt.Errorf("delete reset attachment %s: %w", attachment.Name, err)
		}
	}
	var handle substrate.ResetHandle
	err := e.recordSubstep(item.step, "prepare_reset", nil, func() error {
		var prepareErr error
		handle, prepareErr = item.resetDriver.PrepareReset(ctx, item.request)
		if prepareErr != nil {
			return prepareErr
		}
		raw, marshalErr := item.resetDriver.MarshalResetHandle(handle)
		if marshalErr != nil {
			return fmt.Errorf("encode reset handle %s: %w", item.action.Address, marshalErr)
		}
		item.handle = handle
		if setErr := item.current.SetRuntimeValue("reset_handle", string(raw)); setErr != nil {
			return setErr
		}
		if err := e.recordStepExternal(ctx, item.step, item.action.Address, controlplane.PlanActionReset); err != nil {
			return err
		}
		item.current = e.state.FindResource(item.action.Address)
		if item.current == nil {
			return fmt.Errorf("reset node %s disappeared after persisting prepare state", item.action.Address)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("prepare reset %s: %w", item.action.Address, err)
	}
	return nil
}

func cloneResetStateResource(resource state.Resource) state.Resource {
	cloned := resource
	cloned.Attributes = state.MustAttributes(resource.AttributeMap())
	cloned.Attachments = cloneAttachments(resource.Attachments)
	cloned.Dependencies = append([]address.Address(nil), resource.Dependencies...)
	cloned.Private = append(json.RawMessage(nil), resource.Private...)
	return cloned
}

func restoreResetStateResource(st *state.State, resource state.Resource) {
	if current := st.FindResource(resource.Address); current != nil {
		*current = cloneResetStateResource(resource)
		return
	}
	st.AddResource(cloneResetStateResource(resource))
}

func (e *Executor) applyResetNode(ctx context.Context, item *resetNodeContext) error {
	var handle substrate.NodeHandle
	err := e.recordSubstep(item.step, "apply_reset", nil, func() error {
		var applyErr error
		handle, applyErr = item.resetDriver.ApplyReset(ctx, item.handle)
		return applyErr
	})
	if err != nil {
		return fmt.Errorf("apply reset %s: %w", item.action.Address, err)
	}
	caps := item.nodeDriver.Capabilities()
	if caps.NICHotPlug {
		if err := e.recordSubstep(item.step, "start_node", nil, func() error { return item.nodeDriver.StartNode(ctx, handle) }); err != nil {
			return err
		}
	}
	wired, err := wireNICsWithHook(ctx, item.nicDriver, e.state, handle, item.nicSpecs, item.node.Address, e.substepHook(item.step))
	if err != nil {
		return err
	}
	handle.Net.PrimaryIP = wired.PrimaryIP
	var guestInit driver.GuestNetworkInit
	if len(caps.GuestNetworkInitModes) > 0 {
		guestInit, err = driver.DefaultRegistry.RequireGuestNetworkInit(item.current.Driver)
		if err != nil {
			return err
		}
		if err := e.recordSubstep(item.step, "prepare_guest_network", nil, func() error { return guestInit.PrepareGuestNetwork(ctx, handle) }); err != nil {
			return err
		}
	}
	if !caps.NICHotPlug {
		if err := e.recordSubstep(item.step, "start_node", nil, func() error { return item.nodeDriver.StartNode(ctx, handle) }); err != nil {
			return err
		}
	}
	if guestInit != nil {
		var observation substrate.GuestNetworkInitObservation
		err := e.recordSubstep(item.step, "observe_guest_network", nil, func() error {
			var observeErr error
			observation, observeErr = guestInit.ObserveGuestNetwork(ctx, handle)
			return observeErr
		})
		if err != nil {
			return err
		}
		if !observation.Converged {
			return fmt.Errorf("guest network for %s did not converge: %s", item.node.Address, observation.Reason)
		}
	}
	if err := item.nodeDriver.PrepareHandle(ctx, &handle, item.request.Node.ProviderConfig, stateAdapter{e.state}); err != nil {
		return err
	}
	providerState, err := item.stateDriver.MarshalProviderState(handle)
	if err != nil {
		return err
	}
	attributes := item.current.AttributeMap()
	delete(attributes, "container_id")
	attributes["primary_ip"] = handle.Net.PrimaryIP
	attributes["ports"] = resolvePorts(item.request.Node.Ports, handle.Net.PrimaryIP)
	updated := *item.current
	updated.Attributes = state.MustAttributes(attributes)
	updated.Attachments = wired.Attachments
	updated.Status = state.ResourcePresent
	if err := updated.SetRuntimeValue("container_id", handle.ID); err != nil {
		return err
	}
	updated.ExternalID = handle.ID
	if len(providerState) > 0 {
		if err := updated.SetProviderState(providerState); err != nil {
			return err
		}
	}
	if len(item.config.Provisioners) > 0 {
		connection, err := connectionForNode(ctx, item.nodeDriver, handle, item.config.Connections)
		if err != nil {
			return err
		}
		if waiter, ok := connection.(substrate.ConnectionWaiter); ok {
			if err := waiter.WaitReady(ctx, 60*time.Second); err != nil {
				return err
			}
		}
		if err := e.recordSubstep(item.step, "run_provisioners", nil, func() error { return e.runProvisioners(ctx, connection, item.config.Provisioners) }); err != nil {
			return err
		}
	}
	if err := e.recordSubstep(item.step, "exec_image_entry", nil, func() error { return e.execImageEntry(ctx, handle, item.current.Driver) }); err != nil {
		return err
	}
	var observation substrate.ResetObservation
	err = e.recordSubstep(item.step, "observe_reset", nil, func() error {
		var observeErr error
		observation, observeErr = item.resetDriver.ObserveReset(ctx, item.handle)
		return observeErr
	})
	if err != nil {
		return fmt.Errorf("observe reset %s: %w", item.action.Address, err)
	}
	if !observation.Converged {
		return fmt.Errorf("reset %s did not converge: %s", item.action.Address, observation.Reason)
	}
	if len(observation.Residue) > 0 {
		return fmt.Errorf("reset %s left owned residue: %v", item.action.Address, observation.Residue)
	}
	if err := e.recordSubstep(item.step, "refresh_node", nil, func() error {
		nodeObservation, observeErr := item.nodeDriver.ObserveNode(ctx, handle)
		if observeErr != nil {
			return observeErr
		}
		decision := DecideNodeRecovery(RecoveryInput{
			Context: RecoveryContextRefresh, ResourceType: item.node.Address.Type,
			Provider: item.current.Driver, HasState: true, Observation: nodeObservation,
		})
		if decision.Decision != controlplane.RecoveryDecisionNoop {
			return fmt.Errorf("final health did not converge: %s", decision.Reason)
		}
		status, reason, attachmentErr := observeAttachments(ctx, handle, &updated)
		if attachmentErr != nil {
			return attachmentErr
		}
		if status != state.ResourcePresent {
			return fmt.Errorf("final attachment health did not converge: %s", reason)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("refresh reset %s: %w", item.action.Address, err)
	}
	if err := e.recordSubstep(item.step, "cleanup_reset", nil, func() error { return item.resetDriver.CleanupReset(ctx, item.handle) }); err != nil {
		return fmt.Errorf("cleanup reset %s: %w", item.action.Address, err)
	}
	if err := updated.SetRuntimeValue("reset_handle", ""); err != nil {
		return err
	}
	*item.current = updated
	return nil
}
