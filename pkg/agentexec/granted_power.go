package agentexec

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
)

// Remote power operations never reconstruct provider credentials in the Agent.
func executeGrantedNodeOperation(ctx context.Context, opts Options, cmd controlplane.AgentCommand) controlplane.NodeOperation {
	op := cmd.Operation
	op.Status = controlplane.NodeOperationStatusRunning
	op.StartedAt = time.Now().UTC()
	op.Audit = append(op.Audit, controlplane.NodeOperationEvent(op, "start", op.Status, "node operation started"))
	var err error
	if cmd.Operation.Operation == "adoption_inspect" {
		err = runGrantedInspection(ctx, opts, cmd)
	} else if controlplane.IsGuestSSHAccessOperation(cmd.Operation.Operation) || cmd.Operation.Operation == "adoption_apply" {
		if cmd.Operation.Operation == "adoption_apply" {
			err = runGrantedAdoption(ctx, opts, cmd)
		} else {
			err = runGrantedSSHAccess(ctx, opts, cmd)
		}
	} else {
		err = runGrantedPower(ctx, opts, cmd)
	}
	op.EndedAt = time.Now().UTC()
	if err != nil {
		op.Status = controlplane.NodeOperationStatusFailed
		op.Err = err.Error()
	} else {
		op.Status = controlplane.NodeOperationStatusDone
		op.Err = ""
	}
	op.Audit = append(op.Audit, controlplane.NodeOperationEvent(op, "complete", op.Status, op.Err))
	return op
}

func runGrantedAdoption(ctx context.Context, opts Options, cmd controlplane.AgentCommand) error {
	if opts.HostSupervisorSocket == "" || opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return fmt.Errorf("adoption requires host supervisor and execution session")
	}
	req, err := controlplane.FromAdoptionCommand(cmd)
	if err != nil {
		return err
	}
	key, err := os.ReadFile(opts.ExecutionGrantPublicKeyFile)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize {
		return controlplane.ErrInvalidExecutionGrant
	}
	target := hostsupervisor.TargetRef{Topology: req.Topology, Node: cmd.Operation.Node, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, target)
	boot, err := client.AdoptionHostBootID(ctx)
	if err != nil {
		return err
	}
	var grant controlplane.SignedExecutionGrant
	endpoint := opts.APIURL + "/v1/agents/" + opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	if err = post(ctx, opts, endpoint, map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return err
	}
	if err = controlplane.VerifyExecutionGrant(key, grant, boot, time.Now()); err != nil {
		return err
	}
	digest, err := req.Digest()
	if err != nil {
		return err
	}
	if grant.Grant.Operation != "adoption_apply" || grant.Grant.Digest != digest || grant.Grant.Scope != target.Scope || grant.Grant.TargetBootID != boot {
		return controlplane.ErrInvalidExecutionGrant
	}
	if _, err = client.ApplyAdoption(ctx, req, grant); err == nil {
		return nil
	}
	observeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = client.ObserveAdoption(observeCtx, req, grant); err != nil {
		return fmt.Errorf("adoption outcome unconfirmed: %w", err)
	}
	return nil
}

func runGrantedSSHAccess(ctx context.Context, opts Options, cmd controlplane.AgentCommand) error {
	if opts.HostSupervisorSocket == "" || opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return fmt.Errorf("ssh access requires host supervisor and execution session")
	}
	req, err := controlplane.FromGuestSSHAccessCommand(cmd)
	if err != nil {
		return err
	}
	key, err := os.ReadFile(opts.ExecutionGrantPublicKeyFile)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize {
		return controlplane.ErrInvalidExecutionGrant
	}
	target := hostsupervisor.TargetRef{Topology: cmd.Operation.Topology, Node: cmd.Operation.Node, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, target)
	discovery, err := client.DiscoverManagedSSHAccess(ctx, req.Config.AccessID)
	if err != nil {
		return err
	}
	if discovery.Query.User != req.Config.User {
		return controlplane.ErrInvalidExecutionGrant
	}
	boot := discovery.BootID
	var grant controlplane.SignedExecutionGrant
	endpoint := opts.APIURL + "/v1/agents/" + opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	if err = post(ctx, opts, endpoint, map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return err
	}
	if err = controlplane.VerifyExecutionGrant(key, grant, boot, time.Now()); err != nil {
		return err
	}
	digest, err := req.Digest()
	if err != nil {
		return err
	}
	if cmd.Attempt <= 0 || grant.Grant.AgentID != opts.ID || cmd.AgentID != opts.ID || grant.Grant.Attempt != uint64(cmd.Attempt) || grant.Grant.TargetBootID != boot || grant.Grant.Scope != target.Scope || grant.Grant.SessionID != opts.ExecutionSessionID || grant.Grant.CommandID != cmd.ID || grant.Grant.ActionID != cmd.ActionID || grant.Grant.Digest != digest || grant.Grant.Operation != cmd.Operation.Operation {
		return controlplane.ErrInvalidExecutionGrant
	}
	_, callErr := client.MutateGuestSSHAccess(ctx, req, grant)
	if callErr == nil {
		return nil
	}
	observeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = client.ObserveGuestSSHAccess(observeCtx, req, grant); err != nil {
		return fmt.Errorf("ssh access outcome unconfirmed: dispatch: %v; observe: %w", callErr, err)
	}
	return nil
}

func runGrantedPower(ctx context.Context, opts Options, cmd controlplane.AgentCommand) error {
	if opts.HostSupervisorSocket == "" {
		return fmt.Errorf("remote node operation requires a host supervisor socket")
	}
	request, err := controlplane.FromNodeOperationCommand(cmd)
	if err != nil {
		return err
	}
	if opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return fmt.Errorf("remote node operation requires authenticated execution session and issuer key")
	}
	key, err := os.ReadFile(opts.ExecutionGrantPublicKeyFile)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize {
		return controlplane.ErrInvalidExecutionGrant
	}
	target := hostsupervisor.TargetRef{Topology: request.Topology, Node: request.Node, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, target)
	boot, err := client.HostBootID(ctx)
	if err != nil {
		return err
	}
	var grant controlplane.SignedExecutionGrant
	endpoint := opts.APIURL + "/v1/agents/" + opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	if err := post(ctx, opts, endpoint, map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return err
	}
	if err := controlplane.VerifyExecutionGrant(key, grant, boot, time.Now()); err != nil {
		return err
	}
	digest, err := request.Digest()
	if err != nil {
		return err
	}
	g := grant.Grant
	if g.TargetBootID != boot || g.Scope != target.Scope || g.AgentID != opts.ID || g.SessionID != opts.ExecutionSessionID || g.CommandID != cmd.ID || g.ActionID != cmd.ActionID || g.Attempt != uint64(cmd.Attempt) || g.Operation != "node_power" || g.Digest != digest {
		return controlplane.ErrInvalidExecutionGrant
	}
	if err := client.ExecutePower(ctx, request, grant); err != nil {
		// One read-only recovery after a lost response. Never repeat the effect.
		observeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if observeErr := client.ObservePower(observeCtx, grant); observeErr != nil {
			return fmt.Errorf("power operation outcome unconfirmed: %w", err)
		}
	}
	return nil
}
