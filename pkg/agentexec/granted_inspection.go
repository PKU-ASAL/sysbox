package agentexec

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"os"
	"time"
)

func runGrantedInspection(ctx context.Context, opts Options, cmd controlplane.AgentCommand) error {
	if opts.HostSupervisorSocket == "" || opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return fmt.Errorf("adoption inspection requires owning host and execution session")
	}
	req, err := controlplane.FromAdoptionInspectionCommand(cmd)
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
	target := hostsupervisor.TargetRef{Topology: req.Topology, Node: cmd.Operation.Node, Scope: controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, target)
	boot, err := client.AdoptionHostBootID(ctx)
	if err != nil {
		return err
	}
	var signed controlplane.SignedExecutionGrant
	if err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/commands/"+cmd.ID+"/execution-grant", map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &signed); err != nil {
		return err
	}
	if err := controlplane.VerifyExecutionGrant(key, signed, boot, time.Now()); err != nil {
		return err
	}
	digest, err := req.Digest()
	if err != nil {
		return err
	}
	g := signed.Grant
	if cmd.AgentID != opts.ID || g.AgentID != opts.ID || g.CommandID != cmd.ID || g.ActionID != req.ID || g.SessionID != opts.ExecutionSessionID || g.Attempt != uint64(cmd.Attempt) || g.Operation != "adoption_inspect" || g.Digest != digest || g.Scope != target.Scope || g.TargetBootID != boot {
		return controlplane.ErrInvalidExecutionGrant
	}
	_, err = client.InspectAdoption(ctx, req, signed)
	return err // Host cold recovery reports durable evidence; worker never fabricates it.
}
