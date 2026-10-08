package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	firecrackerprovider "github.com/oslab/sysbox/pkg/provider/firecracker"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	runBridge := api.NewExecutionBridge(cfg)
	runExecutor := agentexec.NewExecutorWithBridge(runBridge)
	dispatcher := &hostsupervisor.Dispatcher{Root: cfg.HostSupervisor.Root, AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
	power := &hostsupervisor.PowerDispatcher{Root: filepath.Join(cfg.HostSupervisor.Root, "power"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.ResolveNode, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
	files := &hostsupervisor.FileDispatcher{Dispatcher: &hostsupervisor.Dispatcher{Root: filepath.Join(cfg.HostSupervisor.Root, "files"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}, Fetch: authority.FetchFile}
	runCoordinator := &hostsupervisor.RunCoordinator{Root: filepath.Join(cfg.HostSupervisor.Root, "runs"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Authorize: authority.Authorize}
	runCoordinator.ReportRunID = func(ctx context.Context, runID string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
		return authority.ReportRun(ctx, runID, receipt, evidence)
	}
	server := hostsupervisor.Server{Socket: cfg.HostSupervisor.Socket, GroupID: cfg.HostSupervisor.WorkerGroupID, Dispatcher: dispatcher, Power: power, Files: files, OnRecoveryError: func(err error) { fmt.Fprintf(cmd.ErrOrStderr(), "host supervisor reconciliation: %v\n", err) }}
	server.Runs = runCoordinator
	server.RunEffect = func(ctx context.Context, req hostsupervisor.RunRequest) (json.RawMessage, error) {
		if req.ExpectedSerial < 1 {
			return nil, fmt.Errorf("host run requires a positive expected state serial")
		}
		path, err := runBridge.MaterializeRunRevision(ctx, req.Topology, req.Revision)
		if err != nil {
			return nil, fmt.Errorf("materialize immutable host revision: %w", err)
		}
		manager, err := runBridge.StateManager(req.Topology)
		if err != nil {
			return nil, err
		}
		canonical, err := manager.LoadWithContext(ctx)
		if err != nil || canonical == nil || canonical.TopologyUID != req.TopologyUID || canonical.Meta.Serial != req.ExpectedSerial {
			return nil, fmt.Errorf("host run canonical state changed")
		}
		if req.ResourceUID != "" {
			addr, parseErr := address.Parse(req.Target)
			if parseErr != nil {
				return nil, parseErr
			}
			resource := canonical.FindResource(addr)
			if resource == nil || resource.OwnerUID != req.TopologyUID || resource.ResourceUID != req.ResourceUID || resource.Generation != req.Generation {
				return nil, fmt.Errorf("host run target identity changed")
			}
		}
		runCtx := transport.WithTaskGrantResolver(ctx, func(taskCtx context.Context, taskID string, taskReq substrate.ExecRequest, identity substrate.OperationIdentity) (controlplane.SignedExecutionGrant, error) {
			parent, ok := transport.ParentExecutionGrantFromContext(taskCtx)
			if !ok {
				return controlplane.SignedExecutionGrant{}, fmt.Errorf("host run parent grant unavailable")
			}
			latest, loadErr := manager.LoadWithContext(taskCtx)
			if loadErr != nil || latest == nil || latest.TopologyUID != identity.TopologyUID {
				return controlplane.SignedExecutionGrant{}, fmt.Errorf("guest task canonical state unavailable")
			}
			var resource *state.Resource
			for i := range latest.Resources {
				if latest.Resources[i].ResourceUID == identity.ResourceUID {
					resource = &latest.Resources[i]
					break
				}
			}
			if resource == nil || resource.OwnerUID != latest.TopologyUID || resource.Generation != identity.Generation {
				return controlplane.SignedExecutionGrant{}, fmt.Errorf("guest task resource identity changed")
			}
			target, err := resolver.Resolve(taskCtx, hostsupervisor.TargetRef{Topology: req.Topology, Node: resource.Address.Name, Scope: controlplane.ExecutionScope{TopologyUID: identity.TopologyUID, ResourceUID: identity.ResourceUID, Generation: identity.Generation}})
			if err != nil {
				return controlplane.SignedExecutionGrant{}, err
			}
			bootProvider, ok := target.Connection.(interface {
				GuestTaskBootID(context.Context) (string, error)
			})
			if !ok {
				return controlplane.SignedExecutionGrant{}, fmt.Errorf("host run target has no guest task boot identity")
			}
			bootID, err := bootProvider.GuestTaskBootID(taskCtx)
			if err != nil {
				return controlplane.SignedExecutionGrant{}, err
			}
			task, err := transport.BuildTaskRequest(taskCtx, taskID, taskReq)
			if err != nil {
				return controlplane.SignedExecutionGrant{}, err
			}
			digest, err := task.Digest()
			if err != nil {
				return controlplane.SignedExecutionGrant{}, err
			}
			return authority.DeriveGuestGrant(taskCtx, parent, taskID, digest, identity.ResourceUID, identity.Generation, bootID)
		})
		run := &controlplane.Run{ID: req.RunID, Topology: req.Topology, Revision: req.Revision, SnapshotPath: path, Op: req.Action, Operation: req.Action, Target: req.Target, Status: controlplane.RunRunning, AgentID: ident.ID, StartedAt: time.Now().UTC(), QueuedAt: time.Now().UTC(), Attempt: 1, LeaseOwner: "host-supervisor"}
		runExecutor.ExecuteContext(runCtx, run)
		if run.Status != controlplane.RunDone {
			return nil, fmt.Errorf("host run failed: %s", run.Err)
		}
		return json.Marshal(run)
	}
	server.SSH = &hostsupervisor.SSHAccessCoordinator{Root: filepath.Join(cfg.HostSupervisor.Root, "ssh-access"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Report: authority.ReportNodeOperation, Authorize: authority.Authorize}
	server.SSHApply = func(ctx context.Context, req controlplane.SSHAccessRequest, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		target, err := resolver.Resolve(ctx, hostsupervisor.TargetRef{Topology: req.Topology, Node: req.Node, Scope: grant.Grant.Scope})
		if err != nil {
			return nil, err
		}
		copier, ok := target.Connection.(substrate.ConnectionFileCopier)
		if !ok {
			return nil, fmt.Errorf("ssh access target has no file installer")
		}
		data := []byte(strings.Join(req.AuthorizedKeys, "\n") + "\n")
		tmp, err := os.CreateTemp("", "sysbox-ssh-supervisor-")
		if err != nil {
			return nil, err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if err := tmp.Chmod(0600); err != nil {
			tmp.Close()
			return nil, err
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			return nil, err
		}
		path, err := hostsupervisor.ManagedSSHPath(target.User, req.ID)
		if err != nil {
			return nil, err
		}
		if err := copier.CopyFile(ctx, name, path); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"path": path, "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	server.SSHDelete = func(ctx context.Context, req controlplane.SSHAccessRequest, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		target, err := resolver.Resolve(ctx, hostsupervisor.TargetRef{Topology: req.Topology, Node: req.Node, Scope: grant.Grant.Scope})
		if err != nil {
			return nil, err
		}
		execer, ok := target.Connection.(interface {
			Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error)
		})
		if !ok {
			return nil, fmt.Errorf("ssh access target has no command verifier")
		}
		path, err := hostsupervisor.ManagedSSHPath(target.User, req.ID)
		if err != nil {
			return nil, err
		}
		r, err := execer.Exec(ctx, substrate.ExecRequest{Program: "/bin/rm", Args: []string{"-f", path}, Shell: substrate.ShellNone}, nil, nil)
		if err != nil || !r.ExitCodeKnown || r.ExitCode != 0 {
			return nil, fmt.Errorf("ssh access delete failed: %w", err)
		}
		return json.RawMessage(`{"deleted":true}`), nil
	}
	server.Adoption = &hostsupervisor.AdoptionCoordinator{Root: filepath.Join(cfg.HostSupervisor.Root, "adoption"), AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Report: authority.ReportNodeOperation, Authorize: authority.Authorize}
	server.AdoptionApply = func(ctx context.Context, req controlplane.AdoptionRequest, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		p, err := driver.DefaultRegistry.RequireOwnershipAdoption(req.Provider)
		if err != nil {
			return nil, err
		}
		identity := substrate.OperationIdentity{ActionID: req.ID, OwnerUID: req.TopologyUID, TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}
		ctx = substrate.WithOperationIdentity(ctx, identity)
		if _, err := p.ApplyAdoption(ctx, req.Evidence); err != nil {
			return nil, err
		}
		h, err := p.ObserveAdoption(ctx, req.Evidence)
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
	// GC effects stay in this credential-owning process. The current concrete
	// provider callback covers Docker; unsupported provider kinds fail closed in
	// the supervisor rather than falling back to the CLI's raw delete path.
	if docker, dockerErr := dockerprovider.New(); dockerErr == nil {
		defer docker.Close()
		gc := &hostsupervisor.GCCoordinator{
			Root: filepath.Join(cfg.HostSupervisor.Root, "gc"), AgentID: ident.ID,
			HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key,
			Authorize: authority.Authorize, ReportStop: authority.ReportStop,
			ValidateInventory: func(context.Context, hostsupervisor.GCCandidate) error { return nil },
		}
		server.GC = gc
		server.GCDelete = func(ctx context.Context, candidate hostsupervisor.GCCandidate, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
			return gc.Delete(ctx, candidate, grant, func(effectCtx context.Context) (json.RawMessage, error) {
				switch candidate.Kind {
				case "container":
					if err := docker.DeleteManagedExternal(effectCtx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
						return nil, err
					}
				case "network":
					if err := docker.DeleteManagedNetworkExternal(effectCtx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
						return nil, err
					}
				case "domain":
					lv := libvirtprovider.New(cfg.Providers.Libvirt.Workdir)
					if err := lv.DeleteManagedExternal(effectCtx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
						return nil, err
					}
				case "firecracker":
					fc := firecrackerprovider.New(cfg.Providers.Firecracker.Kernel, cfg.Providers.Firecracker.Workdir)
					if err := fc.DeleteManagedExternal(effectCtx, candidate.ExternalID, candidate.OwnerUID, candidate.ResourceUID, candidate.Generation); err != nil {
						return nil, err
					}
				default:
					return nil, fmt.Errorf("host supervisor GC provider kind %q is not configured", candidate.Kind)
				}
				return json.RawMessage(`{"deleted":true}`), nil
			})
		}
	}
	return server.Serve(cmd.Context())
}}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorConfigFile, "config", config.DefaultConfigPath, "service configuration")
	supervisorCmd.Flags().StringVar(&supervisorIdentityFile, "identity", agent.DefaultIdentityPath, "authenticated Agent identity")
	rootCmd.AddCommand(supervisorCmd)
}
