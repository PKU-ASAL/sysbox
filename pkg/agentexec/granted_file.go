package agentexec

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"os"
	"time"
)

func runGrantedFileInstall(ctx context.Context, opts Options, cmd controlplane.AgentCommand) (substrate.FileInstallResult, error) {
	var result substrate.FileInstallResult
	if opts.HostSupervisorSocket == "" {
		return result, fmt.Errorf("remote file installation requires a host supervisor socket")
	}
	request, err := controlplane.FromFileInstallCommand(cmd)
	if err != nil {
		return result, err
	}
	if opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return result, fmt.Errorf("file installation requires authenticated session and issuer key")
	}
	key, err := os.ReadFile(opts.ExecutionGrantPublicKeyFile)
	if err != nil {
		return result, err
	}
	if len(key) != ed25519.PublicKeySize {
		return result, controlplane.ErrInvalidExecutionGrant
	}
	ref := hostsupervisor.TargetRef{Topology: cmd.FilePut.Topology, Node: cmd.FilePut.Node, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, ref)
	boot, err := client.GuestFileBootID(ctx)
	if err != nil {
		return result, err
	}
	var grant controlplane.SignedExecutionGrant
	endpoint := opts.APIURL + "/v1/agents/" + opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	if err := post(ctx, opts, endpoint, map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return result, err
	}
	if err := controlplane.VerifyExecutionGrant(key, grant, boot, time.Now()); err != nil {
		return result, err
	}
	digest, err := request.Digest()
	if err != nil {
		return result, err
	}
	g := grant.Grant
	if g.Scope != ref.Scope || g.TargetBootID != boot || g.AgentID != opts.ID || g.SessionID != opts.ExecutionSessionID || g.CommandID != cmd.ID || g.ActionID != cmd.ActionID || g.Attempt != uint64(cmd.Attempt) || g.Operation != "guest_file_install" || g.Digest != digest {
		return result, controlplane.ErrInvalidExecutionGrant
	}
	handle := substrate.FileInstallHandle{ID: request.ID, Digest: digest, BootID: boot, NodeID: ref.Node, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, Epoch: g.Epoch}
	result, err = client.InstallGuestFile(ctx, request, grant)
	observeCtx, stop := context.WithDeadline(ctx, g.ExpiresAt)
	defer stop()
	for err != nil || !result.Quiescent || result.Outcome == substrate.FileInstallUnknown {
		if observeCtx.Err() != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			result, err = client.CancelGuestFile(cleanup, handle)
			cancel()
			if err != nil {
				return result, fmt.Errorf("file installation cancellation unconfirmed: %w", err)
			}
			break
		}
		// Recovery is observation-only; the installation request is never replayed.
		result, err = client.ObserveGuestFile(observeCtx, handle)
		if err == nil && result.Quiescent && result.Outcome != substrate.FileInstallUnknown {
			break
		}
		select {
		case <-observeCtx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !result.Quiescent {
		return result, fmt.Errorf("file installation quiescence unconfirmed")
	}
	if result.Outcome != substrate.FileInstallCommitted {
		return result, fmt.Errorf("file installation outcome %s", result.Outcome)
	}
	if result.SHA256 != request.SHA256 || result.Size != request.Size || result.Mode != request.Mode {
		return result, fmt.Errorf("file installation result differs from authorized request")
	}
	return result, nil
}
