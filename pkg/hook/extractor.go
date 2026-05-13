package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/oslab/sysbox/pkg/matcher"
)

// Extractor converts a ToolCall into a Prediction.
type Extractor interface {
	Extract(call ToolCall) matcher.Prediction
}

// ExtractionRule is one rule in a rules/extraction/*.yaml file.
type ExtractionRule struct {
	ID      string          `yaml:"id"`
	Match   ExtractionMatch `yaml:"match"`
	Predict []PredictedEvent `yaml:"predict"`
	TTP     string          `yaml:"ttp"`
	Window  int             `yaml:"window,omitempty"` // override default time window
}

type ExtractionMatch struct {
	// CommandContains lists substrings; any match triggers this rule.
	CommandContains []string `yaml:"command_contains"`
	// ToolName restricts the rule to specific tool names ("bash_exec", etc.)
	ToolName []string `yaml:"tool_name,omitempty"`
}

type PredictedEvent struct {
	Event string         `yaml:"event"`
	Args  map[string]any `yaml:"args,omitempty"`
}

// RuleExtractor is the Phase 3.0 pure-rule implementation.
// Phase 3.1 will add a small LLM (Qwen-7B) for novel exploit generalization.
type RuleExtractor struct {
	rules []ExtractionRule
}

// Rules returns all loaded extraction rules (used by CLI status output).
func (e *RuleExtractor) Rules() []ExtractionRule { return e.rules }

// NewRuleExtractor loads all YAML rule files from a directory.
func NewRuleExtractor(rulesDir string) (*RuleExtractor, error) {
	e := &RuleExtractor{}
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return e, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(rulesDir, entry.Name())
		rules, err := loadExtractionRules(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", path, err)
		}
		e.rules = append(e.rules, rules...)
	}
	return e, nil
}

// Extract finds the first matching rule and builds a Prediction.
// If no rule matches, returns a Prediction with no ExpectedEvents
// (Matcher treats it as a zero-event prediction → hit rate = 1.0 by convention).
func (e *RuleExtractor) Extract(call ToolCall) matcher.Prediction {
	pred := matcher.Prediction{
		RunID:       call.RunID,
		AgentStep:   call.AgentStep,
		Node:        call.Node,
		SubmittedAt: time.Now(),
		TimeWindow:  30,
		ToolCall:    call.Command,
	}

	for _, rule := range e.rules {
		if !ruleMatchesCall(rule, call) {
			continue
		}
		pred.ExtractorRule = rule.ID
		pred.TTP = rule.TTP
		if rule.Window > 0 {
			pred.TimeWindow = rule.Window
		}
		for _, pe := range rule.Predict {
			pred.ExpectedEvents = append(pred.ExpectedEvents, matcher.ExpectedEvent{
				Name: pe.Event,
				Args: pe.Args,
			})
		}
		return pred
	}
	return pred
}

func ruleMatchesCall(rule ExtractionRule, call ToolCall) bool {
	// Tool name filter (optional).
	if len(rule.Match.ToolName) > 0 {
		found := false
		for _, name := range rule.Match.ToolName {
			if strings.EqualFold(name, call.ToolName) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Command must contain at least one substring.
	if len(rule.Match.CommandContains) > 0 {
		for _, sub := range rule.Match.CommandContains {
			if strings.Contains(call.Command, sub) {
				return true
			}
		}
		return false
	}
	return true
}

func loadExtractionRules(path string) ([]ExtractionRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []ExtractionRule
	return rules, yaml.Unmarshal(data, &rules)
}
