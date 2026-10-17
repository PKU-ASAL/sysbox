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

func runGrantedGC(ctx context.Context, opts Options, cmd controlplane.AgentCommand) error {
	if opts.HostSupervisorSocket == "" || opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" || cmd.AgentID != opts.ID || cmd.Type != "gc_delete" || cmd.Session == nil || cmd.Session.Topology == "" || cmd.ID == "" || cmd.ActionID != cmd.ID || cmd.Attempt < 1 || cmd.LeaseOwner == "" || !cmd.LeaseUntil.After(time.Now()) {
		return fmt.Errorf("GC requires owning host and exact durable command lease")
	}
	candidate := controlplane.GCCandidate{Kind: cmd.GCKind, ExternalID: cmd.GCExternalID, TopologyUID: cmd.TopologyUID, OwnerUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, InventoryDigest: cmd.GCInventoryDigest}
	digest, err := candidate.Digest()
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
	target := hostsupervisor.TargetRef{Topology: cmd.Session.Topology, Node: cmd.ResourceUID, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, target)
	ctx, cancel := context.WithDeadline(ctx, cmd.LeaseUntil)
	defer cancel()
	boot, err := client.GCHostBootID(ctx)
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
	g := signed.Grant
	if g.Scope != target.Scope || g.TargetBootID != boot || g.Operation != "gc_delete" || g.Digest != digest || g.AgentID != opts.ID || g.SessionID != opts.ExecutionSessionID || g.CommandID != cmd.ID || g.ActionID != cmd.ActionID || g.Attempt != uint64(cmd.Attempt) {
		return controlplane.ErrInvalidExecutionGrant
	}
	_, effectErr := client.DeleteGC(ctx, candidate, signed)
	if effectErr == nil {
		return nil
	}
	observe, cancelObserve := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelObserve()
	if _, err := client.ObserveGC(observe, candidate, signed); err != nil {
		return fmt.Errorf("GC outcome unconfirmed: dispatch: %v; observation: %w", effectErr, err)
	}
	return nil
}
