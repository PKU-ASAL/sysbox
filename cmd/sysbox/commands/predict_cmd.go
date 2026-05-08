package commands

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/hook"
	"github.com/oslab/sysbox/pkg/matcher"
)

var predictCmd = &cobra.Command{
	Use:   "predict",
	Short: "Manage agent tool-call predictions",
}

var predictListCmd = &cobra.Command{
	Use:   "list",
	Short: "List predictions recorded in predictions.jsonl",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := predictionsPath()
		preds, err := matcher.ReadPredictions(path)
		if err != nil {
			return err
		}
		if len(preds) == 0 {
			fmt.Println("No predictions recorded. Run sysbox sensor start and hook your agent.")
			return nil
		}
		fmt.Printf("%-4s %-20s %-12s %-14s %s\n", "STEP", "NODE", "TTP", "RULE", "TOOL_CALL")
		for _, p := range preds {
			call := p.ToolCall
			if len(call) > 40 {
				call = call[:37] + "..."
			}
			fmt.Printf("%-4d %-20s %-12s %-14s %s\n",
				p.AgentStep, p.Node, p.TTP, p.ExtractorRule, call)
		}
		return nil
	},
}

var (
	predictNode    string
	predictStep    int
	predictCommand string
	predictRunID   string
	predictTTP     string
	predictWindow  int
)

var predictSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Manually submit a prediction (for debugging; normally done by hook layer)",
	RunE: func(cmd *cobra.Command, args []string) error {
		rulesDir := filepath.Join(findRulesDir(), "extraction")
		ext, err := hook.NewRuleExtractor(rulesDir)
		if err != nil {
			return fmt.Errorf("load extraction rules: %w", err)
		}

		call := hook.ToolCall{
			ToolName:  "bash_exec",
			Command:   predictCommand,
			Node:      predictNode,
			RunID:     predictRunID,
			AgentStep: predictStep,
		}
		pred := ext.Extract(call)
		if predictTTP != "" {
			pred.TTP = predictTTP
		}
		if predictWindow > 0 {
			pred.TimeWindow = predictWindow
		}
		pred.SubmittedAt = time.Now()

		path := predictionsPath()
		if err := matcher.AppendPrediction(path, pred); err != nil {
			return err
		}

		fmt.Printf("Prediction recorded:\n")
		fmt.Printf("  step=%d node=%s ttp=%s rule=%s\n",
			pred.AgentStep, pred.Node, pred.TTP, pred.ExtractorRule)
		fmt.Printf("  expected_events=%d window=%ds\n",
			len(pred.ExpectedEvents), pred.TimeWindow)
		return nil
	},
}

func init() {
	predictSubmitCmd.Flags().StringVar(&predictNode, "node", "", "target node name")
	predictSubmitCmd.Flags().IntVar(&predictStep, "step", 0, "agent step number")
	predictSubmitCmd.Flags().StringVar(&predictCommand, "command", "", "tool command (used for rule matching)")
	predictSubmitCmd.Flags().StringVar(&predictRunID, "run-id", "manual", "run ID")
	predictSubmitCmd.Flags().StringVar(&predictTTP, "ttp", "", "MITRE ATT&CK TTP (overrides rule default)")
	predictSubmitCmd.Flags().IntVar(&predictWindow, "window", 0, "time window seconds (0 = rule default)")
	_ = predictSubmitCmd.MarkFlagRequired("node")
	_ = predictSubmitCmd.MarkFlagRequired("command")

	predictCmd.AddCommand(predictListCmd, predictSubmitCmd)
}

func predictionsPath() string {
	return filepath.Join(filepath.Dir(flagStateFile), "predictions.jsonl")
}

func findRulesDir() string {
	// Look for rules/ relative to the binary location or current directory.
	candidates := []string{"rules", "../rules", "../../rules"}
	for _, c := range candidates {
		if _, err := fmt.Sscanf(c, "%s", new(string)); err == nil {
			return c
		}
	}
	return "rules"
}

func asStringFromArgs(args []string) string {
	return strings.Join(args, " ")
}
