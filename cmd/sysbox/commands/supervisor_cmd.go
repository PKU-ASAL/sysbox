package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/substrate"
)

var supervisorConfigFile, supervisorIdentityFile string
var supervisorCmd = &cobra.Command{Use: "host-supervisor", Short: "Serve the host-local signed guest task boundary", RunE: func(cmd *cobra.Command, _ []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("host supervisor must run as root independently of the unprivileged Agent")
	}
	if err := hostsupervisor.ValidateOwnedPath(supervisorConfigFile, false); err != nil {
		return err
	}
	if err := hostsupervisor.ValidateOwnedPath(supervisorIdentityFile, true); err != nil {
		return err
	}
	cfg, err := config.LoadServiceConfig(supervisorConfigFile)
	if err != nil {
		return err
	}
	ident, err := agent.LoadIdentity(supervisorIdentityFile)
	if err != nil {
		return err
	}
	if ident.ID == "" || ident.Secret == "" {
		return fmt.Errorf("host supervisor requires authenticated Agent identity")
	}
	if cfg.HostSupervisor.Socket == "" || cfg.HostSupervisor.Root == "" {
		return fmt.Errorf("host_supervisor socket and private root required")
	}
	if cfg.HostSupervisor.WorkerGroupID <= 0 {
		return fmt.Errorf("host supervisor requires an explicit non-root worker group ID")
	}
	if err := hostsupervisor.ValidateOwnedPath(cfg.Agent.ExecutionGrantPublicKeyFile, false); err != nil {
		return err
	}
	if err := hostsupervisor.ValidateOwnedPath(cfg.HostSupervisor.SigningKeyFile, true); err != nil {
		return err
	}
	if err := hostsupervisor.EnsureOwnedDirectory(cfg.HostSupervisor.Root, 0700); err != nil {
		return err
	}
	public, err := os.ReadFile(cfg.Agent.ExecutionGrantPublicKeyFile)
	if err != nil {
		return err
	}
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid grant issuer public key")
	}
	file, err := os.Open(cfg.HostSupervisor.SigningKeyFile)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("host signing key must be a private regular file")
	}
	key, err := io.ReadAll(io.LimitReader(file, ed25519.PrivateKeySize+1))
	if err != nil {
		return err
	}
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return fmt.Errorf("invalid host signing key")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	authority := hostsupervisor.AuthorityClient{Identity: *ident}
	resolver := hostsupervisor.CanonicalResolver{RunsDir: cfg.Paths.RunsDir, Backend: cfg.State.Backend, AgentID: ident.ID}
	runBridge, err := api.NewExecutionBridge(cfg)
	if err != nil {
		return err
	}
	defer runBridge.Close()
	if err := runBridge.BindHostIdentity(ident.ID, ed25519.PrivateKey(key).Public().(ed25519.PublicKey)); err != nil {
		return err
	}
	resolver.ActionRun = runBridge.ActionRun
	dispatcher := &hostsupervisor.Dispatcher{Root: cfg.HostSupervisor.Root, AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
	dispatcher.ResolveRun = resolver.ResolveRunTask
	dispatcher.FindRunTaskRef = resolver.FindRunTaskRef
	dispatcher.PendingStops = authority.PendingGuestTaskStops
	power := &hostsupervisor.PowerDispatcher{Root: filepath.Join(cfg.HostSupervisor.Root, "power"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.ResolveNode, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
	files := &hostsupervisor.FileDispatcher{Dispatcher: &hostsupervisor.Dispatcher{Root: filepath.Join(cfg.HostSupervisor.Root, "files"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}, Fetch: authority.FetchFile}
	runCoordinator := &hostsupervisor.RunCoordinator{Root: filepath.Join(cfg.HostSupervisor.Root, "runs"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Authorize: authority.Authorize}
	runCoordinator.OnExecutionError = func(req hostsupervisor.RunRequest, err error) {
		fmt.Fprintf(cmd.ErrOrStderr(), "host run %s execution uncertain: %v\n", req.RunID, err)
	}
	runCoordinator.Tasks = dispatcher
	runCoordinator.ReportRunID = func(ctx context.Context, runID string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
		return authority.ReportRun(ctx, runID, receipt, evidence)
	}
	server := hostsupervisor.Server{Socket: cfg.HostSupervisor.Socket, GroupID: cfg.HostSupervisor.WorkerGroupID, Dispatcher: dispatcher, Power: power, Files: files, OnRecoveryError: func(err error) { fmt.Fprintf(cmd.ErrOrStderr(), "host supervisor reconciliation: %v\n", err) }}
	hostCapabilities, _ := agent.DetectCapabilities()
	server.Observer = &hostsupervisor.Observer{AgentID: ident.ID, BootID: strings.TrimSpace(string(boot)), Key: key, Collect: func(ctx context.Context) (controlplane.AgentInventory, []controlplane.ResourceProjection, error) {
		return agentexec.CollectHostObservation(ctx, agentexec.HostObservationOptions{ID: ident.ID, Capabilities: hostCapabilities, Labels: ident.Labels, CapacityPath: cfg.Paths.Home, ArtifactPaths: cfg.Artifacts.Roots}, runBridge)
	}}
	server.Runs = runCoordinator
	hostRun := &agentexec.HostRunExecutor{Bridge: runBridge, Tasks: dispatcher, Resolver: resolver, Authority: authority}
	server.RunEffect = hostRun.Execute
	server.Runs.PrepareRun = hostRun.PrepareRun
	server.Runs.RecoverInterruptedRun = hostRun.RecoverInterruptedRun
	server.RunRecovery = hostRun.Recover
	sshDispatcher := *dispatcher
	sshDispatcher.Root = filepath.Join(cfg.HostSupervisor.Root, "guest-ssh")
	server.SSHGuest = &hostsupervisor.SSHGuestDispatcher{Dispatcher: &sshDispatcher, Report: authority.ReportNodeOperation, ReportDerived: authority.ReportDerivedSSHAccess}
	hostRun.SSH = server.SSHGuest
	server.Adoption = &hostsupervisor.AdoptionCoordinator{Root: filepath.Join(cfg.HostSupervisor.Root, "adoption"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Report: authority.ReportNodeOperation, Authorize: authority.Authorize}
	server.Adoption.AuthorizeFence = authority.AuthorizeAdoptionFence
	server.Adoption.InspectFencedAdoption = func(ctx context.Context, req controlplane.AdoptionRequest) (substrate.NodeObservation, json.RawMessage, error) {
		node, err := driver.DefaultRegistry.RequireNode(req.Provider)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		if req.Provider != "firecracker" {
			locality, ok := node.(interface {
				HostFenceScope(context.Context) (string, error)
			})
			if !ok {
				return substrate.NodeObservation{}, nil, fmt.Errorf("provider fencing locality unavailable")
			}
			scope, err := locality.HostFenceScope(ctx)
			if err != nil {
				return substrate.NodeObservation{}, nil, err
			}
			if scope != "local-only" {
				return substrate.NodeObservation{}, nil, fmt.Errorf("provider is outside fenced host")
			}
		}
		inspector, ok := node.(interface {
			InspectAdoptionRecovery(context.Context, controlplane.AdoptionRequest) (substrate.NodeHandle, error)
		})
		if !ok {
			return substrate.NodeObservation{}, nil, fmt.Errorf("provider ownership recovery unavailable")
		}
		identity := substrate.OperationIdentity{ActionID: req.ID, OwnerUID: req.TopologyUID, TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}
		scoped := substrate.WithOperationIdentity(ctx, identity)
		handle, err := inspector.InspectAdoptionRecovery(scoped, req)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		if handle.ID != req.ExternalID || handle.Generation != req.Generation {
			return substrate.NodeObservation{}, nil, fmt.Errorf("recovery external identity mismatch")
		}
		observed, err := node.ObserveNode(scoped, handle)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		codec, err := driver.DefaultRegistry.RequireNodeState(req.Provider)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		private, err := codec.MarshalProviderState(handle)
		return observed, private, err
	}
	server.Adoption.RecoverFencedAdoption = func(ctx context.Context, proof controlplane.AdoptionDispositionProof, authorize func(context.Context) error) (substrate.NodeObservation, json.RawMessage, error) {
		node, err := driver.DefaultRegistry.RequireNode(proof.Adoption.Provider)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		recoverer, ok := node.(interface {
			RecoverFencedAdoption(context.Context, controlplane.AdoptionDispositionProof, func(context.Context) error) (substrate.NodeHandle, error)
		})
		if !ok {
			return substrate.NodeObservation{}, nil, fmt.Errorf("provider fenced recovery unavailable")
		}
		h, err := recoverer.RecoverFencedAdoption(ctx, proof, authorize)
		if err != nil {
			return substrate.NodeObservation{}, nil, err
		}
		obs, err := node.ObserveNode(ctx, h)
		if err != nil {
			return obs, nil, err
		}
		codec, err := driver.DefaultRegistry.RequireNodeState(proof.Adoption.Provider)
		if err != nil {
			return obs, nil, err
		}
		private, err := codec.MarshalProviderState(h)
		return obs, private, err
	}
	server.Adoption.InspectAdoptionCancellation = func(ctx context.Context, p controlplane.AdoptionDispositionProof) (substrate.NodeObservation, error) {
		node, err := driver.DefaultRegistry.RequireNode(p.Adoption.Provider)
		if err != nil {
			return substrate.NodeObservation{}, err
		}
		if p.Adoption.Provider == "firecracker" {
			inspector, ok := node.(interface {
				InspectAdoptionCancellation(context.Context, controlplane.AdoptionDispositionProof) (substrate.NodeObservation, error)
			})
			if !ok {
				return substrate.NodeObservation{}, fmt.Errorf("provider cancellation observation unavailable")
			}
			return inspector.InspectAdoptionCancellation(ctx, p)
		}
		locality, ok := node.(interface {
			HostFenceScope(context.Context) (string, error)
		})
		if !ok {
			return substrate.NodeObservation{}, fmt.Errorf("provider locality unavailable")
		}
		scope, err := locality.HostFenceScope(ctx)
		if err != nil {
			return substrate.NodeObservation{}, err
		}
		if scope != "local-only" {
			return substrate.NodeObservation{}, fmt.Errorf("provider outside fenced host")
		}
		inspector, ok := node.(interface {
			InspectAdoptionCancellation(context.Context, controlplane.AdoptionRequest) (substrate.NodeObservation, error)
		})
		if !ok {
			return substrate.NodeObservation{}, fmt.Errorf("provider cancellation observation unavailable")
		}
		return inspector.InspectAdoptionCancellation(ctx, p.Adoption)
	}
	server.AdoptionInspect = func(ctx context.Context, req controlplane.AdoptionInspectionRequest) (substrate.AdoptionEvidence, error) {
		provider, err := driver.DefaultRegistry.RequireOwnershipAdoption(req.Provider)
		if err != nil {
			return substrate.AdoptionEvidence{}, err
		}
		return provider.InspectAdoption(ctx, req.ExternalID, substrate.NodeSpec{})
	}
	server.OwnershipPrepare = func(ctx context.Context, plan controlplane.OwnershipTransferPlan) (controlplane.OwnershipPreparation, error) {
		return hostsupervisor.PrepareOwnership(ctx, plan, ident.ID, strings.TrimSpace(string(boot)), key, authority.AuthorizeOwnershipPreparation, func(ctx context.Context, p controlplane.OwnershipTransferPlan) error {
			provider, err := driver.DefaultRegistry.RequireOwnershipAdoption(p.Provider)
			if err != nil {
				return err
			}
			inspector, ok := provider.(interface {
				InspectOwnershipTransfer(context.Context, controlplane.OwnershipTransferPlan) error
			})
			if !ok {
				return fmt.Errorf("provider transfer preparation unavailable")
			}
			return inspector.InspectOwnershipTransfer(ctx, p)
		})
	}
	server.AdoptionApply = func(ctx context.Context, req controlplane.AdoptionRequest, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		p, err := driver.DefaultRegistry.RequireOwnershipAdoption(req.Provider)
		if err != nil {
			return nil, err
		}
		identity := substrate.OperationIdentity{ActionID: req.ID, OwnerUID: req.TopologyUID, TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}
		ctx = substrate.WithOperationIdentity(ctx, identity)
		var h substrate.NodeHandle
		if req.Transfer != nil {
			transfer, ok := p.(interface {
				ApplyOwnershipTransfer(context.Context, controlplane.OwnershipRetirementProof, ed25519.PublicKey) (substrate.NodeHandle, error)
			})
			if !ok {
				return nil, fmt.Errorf("provider ownership transfer unavailable")
			}
			if err := controlplane.VerifyOwnershipRetirement(public, *req.Transfer); err != nil {
				return nil, err
			}
			h, err = transfer.ApplyOwnershipTransfer(ctx, *req.Transfer, public)
		} else {
			if _, err = p.ApplyAdoption(ctx, req.Evidence); err != nil {
				return nil, err
			}
			h, err = p.ObserveAdoption(ctx, req.Evidence)
		}
		if err != nil {
			return nil, err
		}
		codec, err := driver.DefaultRegistry.RequireNodeState(req.Provider)
		if err != nil {
			return nil, err
		}
		if h.ID != req.ExternalID || h.Generation != req.Generation {
			return nil, fmt.Errorf("adoption result identity mismatch")
		}
		evidence, err := codec.MarshalProviderState(h)
		if err != nil {
			return nil, err
		}
		return evidence, nil
	}
	// Recovery and observation do not depend on a provider client. Production
	// deletion revalidates the exact command and its durable reference reservation.
	server.GC = &hostsupervisor.GCCoordinator{
		ClaimExecution: authority.ClaimGCExecution,
		AuthorizeFence: authority.AuthorizeGCFence, InspectFencedCandidate: newSupervisorGCInspection(cfg),
		Root: filepath.Join(cfg.HostSupervisor.Root, "gc"), AgentID: ident.ID,
		HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key,
		Authorize: authority.Authorize, ReportStop: authority.ReportStop,
		ValidateReservation: func(ctx context.Context, candidate hostsupervisor.GCCandidate, grant controlplane.SignedExecutionGrant) error {
			digest, err := candidate.Digest()
			if err != nil {
				return err
			}
			if digest != grant.Grant.Digest {
				return controlplane.ErrInvalidExecutionGrant
			}
			return authority.Authorize(ctx, grant)
		},
	}
	server.GCValidateTarget = supervisorGCReferenceValidator(runBridge.ActionRepository)
	server.GCEffect = newSupervisorGCEffect(cfg)
	server.GCFencedEffect = func(ctx context.Context, candidate hostsupervisor.GCCandidate, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		if candidate.Kind != "firecracker" {
			return server.GCEffect(ctx, candidate)
		}
		fc := firecracker.New(cfg.Providers.Firecracker.Kernel, cfg.Providers.Firecracker.Workdir)
		originalBoot, err := fc.FencedGCManifestBoot(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if originalBoot == "" || originalBoot == grant.Grant.TargetBootID {
			return server.GCEffect(ctx, candidate)
		}
		proof, err := authority.GCFencedCleanupProof(ctx, candidate, grant, originalBoot)
		if err != nil {
			return nil, err
		}
		if proof == nil {
			return server.GCEffect(ctx, candidate)
		}

		if err = fc.DeleteFencedGCResidue(ctx, candidate, *proof, grant, public, ed25519.PrivateKey(key).Public().(ed25519.PublicKey)); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"deleted":true}`), nil
	}

	return server.Serve(cmd.Context())
}}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorConfigFile, "config", config.DefaultConfigPath, "service configuration")
	supervisorCmd.Flags().StringVar(&supervisorIdentityFile, "identity", agent.DefaultIdentityPath, "authenticated Agent identity")
	rootCmd.AddCommand(supervisorCmd)
}
