package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/matcher"
	"github.com/oslab/sysbox/pkg/state"
)

var matchCmd = &cobra.Command{
	Use:   "match",
	Short: "Attribute eBPF events to agent actions via PID tree",
}

var (
	matchEventsFile string
	matchOutputFile string
	matchRunID      string
	matchNodeID     string
	matchAnchorPID  int
	matchAgentName  string
)

var matchRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Filter events by anchor PID descendants → episode_report.json",
	Long: `Reads events.jsonl and returns all events whose PID descends from
the agent's anchor PID (the opencode/agent process inside the attacker node).

The anchor PID is resolved in order:
  1. --anchor-pid <pid>   explicit PID
  2. --agent <name>       looks up sysbox_actor.<name> (or sysbox_agent) in state

Examples:
  sysbox match run --agent red
  sysbox match run --anchor-pid 12345 --node node_attack
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		runDir := filepath.Dir(flagStateFile)

		// Default: per-node events directory; fallback to legacy single file.
		eventsFile := matchEventsFile
		if eventsFile == "" {
			eventsDir := filepath.Join(runDir, "events")
			if fi, err := os.Stat(eventsDir); err == nil && fi.IsDir() {
				eventsFile = eventsDir
			} else {
				eventsFile = filepath.Join(runDir, "events.jsonl")
			}
		}
		outputFile := matchOutputFile
		if outputFile == "" {
			outputFile = filepath.Join(runDir, "episode_report.json")
		}
		runID := matchRunID
		if runID == "" {
			runID = filepath.Base(runDir)
		}

		if _, err := os.Stat(eventsFile); os.IsNotExist(err) {
			return fmt.Errorf("events not found: %s\n  Run: sysbox sensor start", eventsFile)
		}

		anchorPID := matchAnchorPID

		// Resolve from agent state if --agent flag is given.
		if anchorPID == 0 && matchAgentName != "" {
			mgr := state.NewManager(flagStateFile)
			s, err := mgr.Load()
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}
			// Look up sysbox_actor first (new), fall back to sysbox_agent (legacy).
			r := s.FindResource("sysbox_actor", matchAgentName)
			if r == nil {
				r = s.FindResource("sysbox_agent", matchAgentName)
			}
			if r == nil {
				return fmt.Errorf("sysbox_actor.%s not found in state (run sysbox apply first)", matchAgentName)
			}
			pid, _ := r.Instance["pid"].(float64)
			anchorPID = int(pid)
			if matchNodeID == "" {
				matchNodeID, _ = r.Instance["node"].(string)
			}
		}

		if anchorPID == 0 {
			return fmt.Errorf("provide --anchor-pid <pid> or --agent <name>")
		}

		m := matcher.NewMatcher()
		report, err := m.RunFile(anchorPID, eventsFile, matchNodeID, runID)
		if err != nil {
			return fmt.Errorf("match: %w", err)
		}

		if err := report.Save(outputFile); err != nil {
			return fmt.Errorf("save report: %w", err)
		}

		report.PrintSummary()
		fmt.Printf("\nReport written to: %s\n", outputFile)
		return nil
	},
}

var matchReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Print the latest episode_report.json",
	RunE: func(cmd *cobra.Command, args []string) error {
		runDir := filepath.Dir(flagStateFile)
		reportFile := filepath.Join(runDir, "episode_report.json")
		if matchOutputFile != "" {
			reportFile = matchOutputFile
		}

		data, err := os.ReadFile(reportFile)
		if err != nil {
			return fmt.Errorf("read report: %w\n  Run: sysbox match run --agent <name>", err)
		}
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	matchRunCmd.Flags().StringVar(&matchEventsFile, "events", "", "path to events dir or single .jsonl file (default: runs/default/events/)")
	matchRunCmd.Flags().StringVar(&matchOutputFile, "output", "", "output path (default: runs/default/episode_report.json)")
	matchRunCmd.Flags().StringVar(&matchRunID, "run-id", "", "run identifier for the report")
	matchRunCmd.Flags().StringVar(&matchNodeID, "node", "", "filter events by node_id (e.g. node_attack)")
	matchRunCmd.Flags().IntVar(&matchAnchorPID, "anchor-pid", 0, "anchor PID of the agent process (host namespace)")
	matchRunCmd.Flags().StringVar(&matchAgentName, "agent", "", "sysbox_actor (or sysbox_agent) name to look up PID from state")

	matchReportCmd.Flags().StringVar(&matchOutputFile, "file", "", "path to episode_report.json")

	matchCmd.AddCommand(matchRunCmd, matchReportCmd)
}
