package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/matcher"
)

var matchCmd = &cobra.Command{
	Use:   "match",
	Short: "Run Prediction Matcher and IoC Engine against recorded events",
}

var (
	matchEventsFile      string
	matchPredictionsFile string
	matchRulesDir        string
	matchOutputFile      string
	matchRunID           string
)

var matchRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Match predictions against events and produce match_report.json",
	RunE: func(cmd *cobra.Command, args []string) error {
		runDir := filepath.Dir(flagStateFile)

		eventsFile := matchEventsFile
		if eventsFile == "" {
			eventsFile = filepath.Join(runDir, "events.jsonl")
		}
		predsFile := matchPredictionsFile
		if predsFile == "" {
			predsFile = filepath.Join(runDir, "predictions.jsonl")
		}
		rulesDir := matchRulesDir
		if rulesDir == "" {
			rulesDir = findRulesDir()
		}
		outputFile := matchOutputFile
		if outputFile == "" {
			outputFile = filepath.Join(runDir, "match_report.json")
		}
		runID := matchRunID
		if runID == "" {
			runID = filepath.Base(runDir)
		}

		// Verify inputs exist.
		if _, err := os.Stat(eventsFile); os.IsNotExist(err) {
			return fmt.Errorf("events file not found: %s\n  Run: sysbox sensor start", eventsFile)
		}

		// Load IoC rules.
		iocDir := filepath.Join(rulesDir, "ioc")
		ioc, err := matcher.NewIoCEngine(iocDir)
		if err != nil {
			return fmt.Errorf("load IoC rules: %w", err)
		}
		fmt.Printf("Loaded %d IoC rules from %s\n", len(ioc.Rules()), iocDir)

		// Run matcher.
		m := matcher.NewMatcher(ioc)
		report, err := m.RunFile(predsFile, eventsFile, runID)
		if err != nil {
			return fmt.Errorf("match run: %w", err)
		}

		// Save report.
		if err := report.Save(outputFile); err != nil {
			return fmt.Errorf("save report: %w", err)
		}

		report.PrintSummary()
		fmt.Printf("\nMatch report written to: %s\n", outputFile)
		return nil
	},
}

var matchReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Print a summary of the latest match_report.json",
	RunE: func(cmd *cobra.Command, args []string) error {
		runDir := filepath.Dir(flagStateFile)
		reportFile := filepath.Join(runDir, "match_report.json")
		if matchOutputFile != "" {
			reportFile = matchOutputFile
		}

		data, err := os.ReadFile(reportFile)
		if err != nil {
			return fmt.Errorf("read report: %w\n  Run: sysbox match run", err)
		}
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	matchRunCmd.Flags().StringVar(&matchEventsFile, "events", "", "path to events.jsonl (default: runs/default/events.jsonl)")
	matchRunCmd.Flags().StringVar(&matchPredictionsFile, "predictions", "", "path to predictions.jsonl")
	matchRunCmd.Flags().StringVar(&matchRulesDir, "rules", "", "path to rules/ directory")
	matchRunCmd.Flags().StringVar(&matchOutputFile, "output", "", "output path for match_report.json")
	matchRunCmd.Flags().StringVar(&matchRunID, "run-id", "", "run identifier for the report")

	matchReportCmd.Flags().StringVar(&matchOutputFile, "file", "", "path to match_report.json")

	matchCmd.AddCommand(matchRunCmd, matchReportCmd)
}
