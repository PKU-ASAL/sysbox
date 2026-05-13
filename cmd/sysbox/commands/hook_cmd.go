package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/hook"
	pkgstate "github.com/oslab/sysbox/pkg/state"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Claude Code hook integration for IoC extraction",
}

var (
	hookPort   int
	hookRules  string
)

var hookServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the sysbox hook HTTP server (call before starting Claude Code)",
	Long: `Start an HTTP server that intercepts Claude Code PreToolUse events,
extracts IoC predictions from Bash commands, and writes them to predictions.jsonl.

Configure Claude Code to call this server by running:
  sysbox hook install

Then start Claude Code as usual. The hook server records IoC predictions
automatically as Claude executes Bash commands in the field.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load state to enable IP→node resolution.
		mgr := pkgstate.NewManager(flagStateFile)
		st, err := mgr.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[hook] warn: could not load state: %v\n", err)
			st = &pkgstate.State{}
		}

		rulesDir := hookRules
		if rulesDir == "" {
			rulesDir = findRulesDir()
		}
		extractionDir := filepath.Join(rulesDir, "extraction")

		ext, err := hook.NewRuleExtractor(extractionDir)
		if err != nil {
			return fmt.Errorf("load extraction rules from %s: %w", extractionDir, err)
		}
		fmt.Printf("[hook] loaded %d extraction rules from %s\n", len(ext.Rules()), extractionDir)

		predsPath := predictionsPath()
		fmt.Printf("[hook] predictions → %s\n", predsPath)

		srv := hook.NewServer(ext, predsPath, st)

		addr := fmt.Sprintf("127.0.0.1:%d", hookPort)
		fmt.Printf("[hook] listening on http://%s\n", addr)
		fmt.Printf("[hook] ready — start Claude Code and run your agent\n\n")

		return http.ListenAndServe(addr, srv.Handler())
	},
}

var hookInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Write Claude Code hook configuration to .claude/settings.json",
	Long: `Install the sysbox hook configuration into the current project's
.claude/settings.json. This tells Claude Code to call the hook server
on every Bash tool execution.

Run this once per project, then start 'sysbox hook serve' before each session.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		settingsDir := ".claude"
		settingsFile := filepath.Join(settingsDir, "settings.json")

		if err := os.MkdirAll(settingsDir, 0o755); err != nil {
			return err
		}

		// Read existing settings if present.
		existing := map[string]any{}
		if data, err := os.ReadFile(settingsFile); err == nil {
			_ = json.Unmarshal(data, &existing)
		}

		// Build the hooks configuration.
		hookURL := fmt.Sprintf("http://127.0.0.1:%d", hookPort)

		hookConfig := map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "http",
							"url":     hookURL + "/hooks/pre-tool-use",
							"timeout": 5,
						},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "http",
							"url":     hookURL + "/hooks/post-tool-use",
							"timeout": 3,
							"async":   true,
						},
					},
				},
			},
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "http",
							"url":     hookURL + "/hooks/session",
							"timeout": 3,
						},
					},
				},
			},
		}
		existing["hooks"] = hookConfig

		data, _ := json.MarshalIndent(existing, "", "  ")
		if err := os.WriteFile(settingsFile, data, 0o644); err != nil {
			return err
		}

		fmt.Printf("Hook configuration written to %s\n", settingsFile)
		fmt.Printf("\nNext steps:\n")
		fmt.Printf("  1. sysbox hook serve           # in one terminal\n")
		fmt.Printf("  2. claude                      # start Claude Code\n")
		fmt.Printf("  3. (run your agent)\n")
		fmt.Printf("  4. sysbox match run            # score the episode\n")
		return nil
	},
}

var hookStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check hook server status",
	RunE: func(cmd *cobra.Command, args []string) error {
		url := fmt.Sprintf("http://127.0.0.1:%d/hooks/status", hookPort)
		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("hook server not reachable at %s: %w\n  Start it with: sysbox hook serve", url, err)
		}
		defer resp.Body.Close()
		var status map[string]any
		json.NewDecoder(resp.Body).Decode(&status)
		fmt.Printf("Hook server: %s\n", status["status"])
		fmt.Printf("  predictions written: %.0f\n", status["predictions_written"])
		fmt.Printf("  requests handled:    %.0f\n", status["requests_handled"])
		fmt.Printf("  active sessions:     %.0f\n", status["active_sessions"])
		return nil
	},
}

func init() {
	hookServeCmd.Flags().IntVar(&hookPort, "port", 8081, "port to listen on")
	hookServeCmd.Flags().StringVar(&hookRules, "rules", "", "path to rules/ directory")

	hookInstallCmd.Flags().IntVar(&hookPort, "port", 8081, "hook server port")
	hookStatusCmd.Flags().IntVar(&hookPort, "port", 8081, "hook server port")

	hookCmd.AddCommand(hookServeCmd, hookInstallCmd, hookStatusCmd)
}
