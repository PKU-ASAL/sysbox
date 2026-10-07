package commands

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
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
	dispatcher := &hostsupervisor.Dispatcher{Root: cfg.HostSupervisor.Root, AgentID: ident.ID, HostBootID: strings.TrimSpace(string(boot)), IssuerKey: public, HostKey: key, Resolve: resolver.Resolve, Authorize: authority.Authorize, ReportStop: authority.ReportStop}
	server := hostsupervisor.Server{Socket: cfg.HostSupervisor.Socket, GroupID: cfg.HostSupervisor.WorkerGroupID, Dispatcher: dispatcher, OnRecoveryError: func(err error) { fmt.Fprintf(cmd.ErrOrStderr(), "host supervisor reconciliation: %v\n", err) }}
	return server.Serve(cmd.Context())
}}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorConfigFile, "config", config.DefaultConfigPath, "service configuration")
	supervisorCmd.Flags().StringVar(&supervisorIdentityFile, "identity", agent.DefaultIdentityPath, "authenticated Agent identity")
	rootCmd.AddCommand(supervisorCmd)
}
