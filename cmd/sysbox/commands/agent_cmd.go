package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
)

var (
	flagAgentAPI          string
	flagAgentToken        string
	flagAgentID           string
	flagAgentName         string
	flagAgentCapabilities string
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
		ident, err := agent.Register(cmd.Context(), agent.RegisterOptions{
			APIURL:       flagAgentAPI,
			Token:        flagAgentToken,
			ID:           flagAgentID,
			Name:         flagAgentName,
			Capabilities: splitCSV(flagAgentCapabilities),
			Path:         flagAgentIdentity,
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
	Use:   "start",
	Short: "Start the sysbox host agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		ident, err := agent.LoadIdentity(agentIdentityPath())
		if err != nil {
			return err
		}
		cfg, err := config.LoadServiceConfig(flagAgentConfig)
		if err != nil {
			return err
		}
		return agentexec.Run(cmd.Context(), agentexec.Options{
			APIURL:       ident.APIURL,
			Token:        ident.Token,
			ID:           ident.ID,
			Name:         ident.Name,
			Capabilities: ident.Capabilities,
			Labels:       ident.Labels,
			PollInterval: flagAgentPollInterval,
		}, api.NewExecutionBridge(cfg))
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
	agentCmd.AddCommand(agentRegisterCmd, agentStartCmd, agentStatusCmd, agentUnregisterCmd)
	rootCmd.AddCommand(agentCmd)

	agentRegisterCmd.Flags().StringVar(&flagAgentAPI, "api", "http://127.0.0.1:9876", "sysbox control plane API URL")
	agentRegisterCmd.Flags().StringVar(&flagAgentToken, "token", "", "registration/API token")
	agentRegisterCmd.Flags().StringVar(&flagAgentID, "id", "", "agent id")
	agentRegisterCmd.Flags().StringVar(&flagAgentName, "name", "", "agent display name")
	agentRegisterCmd.Flags().StringVar(&flagAgentCapabilities, "capabilities", strings.Join(agent.DefaultCapabilities(), ","), "comma-separated agent capabilities")
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
