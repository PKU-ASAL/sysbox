package commands

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/session"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage sysbox sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List unexpired session expectations from the registry",
	RunE: func(cmd *cobra.Command, args []string) error {
		regPath := filepath.Join(filepath.Dir(flagStateFile), "session-registry.json")
		reg := session.NewRegistry(regPath)
		entries := reg.List()
		if len(entries) == 0 {
			fmt.Println("No active session expectations.")
			return nil
		}
		fmt.Printf("%-20s %-15s %-36s %s\n", "NODE", "SOURCE", "SESSION-ID", "EXPIRES")
		for _, e := range entries {
			src := e.SourceIP
			if src == "" {
				src = "*"
			}
			fmt.Printf("%-20s %-15s %-36s %s\n",
				e.NodeID, src, e.SessionID,
				e.ExpiresAt.Format(time.RFC3339))
		}
		return nil
	},
}

var (
	sessRegNode      string
	sessRegSource    string
	sessRegID        string
	sessRegExpiresIn string
)

var sessionRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Pre-register a session expectation; sshd-hook will use its session_id",
	Long: `Pre-declare a session before the attacker connects.

sysbox session register \
    --node target \
    --session-id exp-abc \
    --expires-in 60s

When the attacker SSH-es into <node> within the expiry window, the
sysbox-sshd-hook picks up the declared session_id instead of generating
a random UUID. This correlates sysbox events with an external trace id
(Langfuse run ID, OTEL trace, etc.).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dur, err := time.ParseDuration(sessRegExpiresIn)
		if err != nil {
			return fmt.Errorf("parse --expires-in: %w", err)
		}
		exp := session.Expectation{
			NodeID:    sessRegNode,
			SourceIP:  sessRegSource,
			SessionID: sessRegID,
			ExpiresAt: time.Now().Add(dur),
		}
		regPath := filepath.Join(filepath.Dir(flagStateFile), "session-registry.json")
		reg := session.NewRegistry(regPath)
		if err := reg.Register(exp); err != nil {
			return err
		}
		fmt.Printf("Registered: %s → %s (expires %s)\n",
			sessRegNode, sessRegID, exp.ExpiresAt.Format(time.RFC3339))
		return nil
	},
}

func init() {
	sessionRegisterCmd.Flags().StringVar(&sessRegNode, "node", "", "target node name")
	sessionRegisterCmd.Flags().StringVar(&sessRegSource, "source", "", "source IP (optional; matches any if empty)")
	sessionRegisterCmd.Flags().StringVar(&sessRegID, "session-id", "", "session ID (e.g. Langfuse run ID)")
	sessionRegisterCmd.Flags().StringVar(&sessRegExpiresIn, "expires-in", "60s", "expiration window")
	_ = sessionRegisterCmd.MarkFlagRequired("node")
	_ = sessionRegisterCmd.MarkFlagRequired("session-id")

	sessionCmd.AddCommand(sessionListCmd, sessionRegisterCmd)
}
