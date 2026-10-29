package commands

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
)

var (
	flagAgentAPI          string
	flagAgentToken        string
	flagAgentID           string
	flagAgentName         string
	flagAgentSubstrates   string
	flagAgentIdentity     string
	flagAgentConfig       string
	flagAgentPollInterval time.Duration
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage the sysbox host agent",
}

var agentRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register this host agent with a control plane",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(splitCSV(flagAgentSubstrates)) == 0 {
			return fmt.Errorf("--substrates is required for enrollment; the host verifies available capabilities")
		}
		ident, err := agent.Register(cmd.Context(), agent.RegisterOptions{
			APIURL:     flagAgentAPI,
			Token:      flagAgentToken,
			ID:         flagAgentID,
			Name:       flagAgentName,
			Substrates: splitCSV(flagAgentSubstrates),
			Path:       flagAgentIdentity,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Agent registered: %s\n", ident.ID)
		fmt.Printf("Identity: %s\n", agentIdentityPath())
		return nil
	},
}

var agentStartCmd = &cobra.Command{
	Annotations: map[string]string{"service-client": "true"},
	Use:         "start",
	Short:       "Start the sysbox host agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := agent.ValidateWorkerPrivileges(); err != nil {
			return err
		}
		if flagAgentConfig == "" {
			return fmt.Errorf("Agent requires an explicit worker-only --config")
		}
		if err := agent.ValidateHostPath(flagAgentConfig, false); err != nil {
			return err
		}
		ident, err := agent.LoadIdentity(agentIdentityPath())
		if err != nil {
			return err
		}
		cfg, err := config.LoadServiceConfig(flagAgentConfig)
		if err != nil {
			return err
		}
		if cfg.API.Token != "" || cfg.State.Backend != "" || cfg.HostSupervisor.SigningKeyFile != "" || cfg.API.ExecutionGrant.SigningKeyFile != "" {
			return fmt.Errorf("worker config must not contain administrator token, database credentials or signing-key paths")
		}
		for _, path := range []string{cfg.Agent.ExecutionGrantPublicKeyFile, cfg.Agent.HostPublicKeyFile} {
			if err := agent.ValidateHostPath(path, false); err != nil {
				return err
			}
		}
		if err := agent.ValidateHostPath(cfg.Agent.HostSupervisorSocket, true); err != nil {
			return err
		}
		sessionFile := cfg.Agent.ExecutionSessionFile
		if sessionFile == "" {
			sessionFile = filepath.Join(cfg.Paths.RunsDir, "_agents", ident.ID, "execution-session.json")
		}
		return agentexec.Run(cmd.Context(), agentexec.Options{
			HostSupervisorSocket:        cfg.Agent.HostSupervisorSocket,
			HostPublicKeyFile:           cfg.Agent.HostPublicKeyFile,
			ExecutionSessionFile:        sessionFile,
			ExecutionGrantPublicKeyFile: cfg.Agent.ExecutionGrantPublicKeyFile,
			APIURL:                      ident.APIURL,
			Token:                       ident.Token,
			ID:                          ident.ID,
			Name:                        ident.Name,
			Capabilities:                ident.Capabilities,
			Labels:                      ident.Labels,
			Secret:                      ident.Secret,
			PollInterval:                flagAgentPollInterval,
			RunRenewInterval:            cfg.RunRenewInterval(),
			RunRenewTTL:                 cfg.RunRenewTTL(),
			Policy:                      cfg.Agent.Policy,
		})
	},
}

var agentStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local agent identity",
	RunE: func(cmd *cobra.Command, args []string) error {
		ident, err := agent.LoadIdentity(agentIdentityPath())
		if err != nil {
			return err
		}
		raw, err := json.MarshalIndent(ident, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	},
}

var agentUnregisterCmd = &cobra.Command{
	Use:   "unregister",
	Short: "Remove local agent identity",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := agent.RemoveIdentity(agentIdentityPath()); err != nil {
			return err
		}
		fmt.Println("Agent identity removed.")
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{agentStatusCmd, agentUnregisterCmd} {
		cmd.Annotations = map[string]string{"service-client": "true"}
	}
	agentCmd.AddCommand(agentRegisterCmd, agentStartCmd, agentStatusCmd, agentUnregisterCmd)
	rootCmd.AddCommand(agentCmd)

	agentRegisterCmd.Flags().StringVar(&flagAgentAPI, "api", "http://127.0.0.1:9876", "sysbox control plane API URL")
	agentRegisterCmd.Flags().StringVar(&flagAgentToken, "token", "", "registration/API token")
	agentRegisterCmd.Flags().StringVar(&flagAgentID, "id", "", "agent id")
	agentRegisterCmd.Flags().StringVar(&flagAgentName, "name", "", "agent display name")
	agentRegisterCmd.Flags().StringVar(&flagAgentSubstrates, "substrates", "", "comma-separated substrates to enroll (required)")
	agentRegisterCmd.Flags().StringVar(&flagAgentIdentity, "identity", agent.DefaultIdentityPath, "local agent identity path")

	agentStartCmd.Flags().StringVar(&flagAgentIdentity, "identity", agent.DefaultIdentityPath, "local agent identity path")
	agentStartCmd.Flags().StringVar(&flagAgentConfig, "config", "", "path to sysbox service YAML config")
	agentStartCmd.Flags().DurationVar(&flagAgentPollInterval, "poll-interval", 2*time.Second, "control-plane poll interval")

	agentStatusCmd.Flags().StringVar(&flagAgentIdentity, "identity", agent.DefaultIdentityPath, "local agent identity path")
	agentUnregisterCmd.Flags().StringVar(&flagAgentIdentity, "identity", agent.DefaultIdentityPath, "local agent identity path")
}

func agentIdentityPath() string {
	if flagAgentIdentity == "" {
		return agent.DefaultIdentityPath
	}
	return flagAgentIdentity
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
