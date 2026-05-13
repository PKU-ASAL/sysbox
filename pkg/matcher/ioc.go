package matcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/oslab/sysbox/pkg/sensor"
)

// IoCRule is one entry in an IoC rule file.
type IoCRule struct {
	ID          string            `yaml:"id"`
	Name        string            `yaml:"name"`
	TTP         string            `yaml:"ttp"`
	Event       string            `yaml:"event"`
	Match       map[string]any    `yaml:"match"`
	// ProcessName optionally restricts the rule to events from specific processes.
	// This enables "shell spawned from web server" detection.
	ProcessName []string          `yaml:"process_name,omitempty"`
}

// IoCEngine scans events against a set of YAML-defined rules.
// It runs independently of the Prediction Matcher so it covers events that
// the hook layer did not predict (unscripted agent behavior).
type IoCEngine struct {
	rules []IoCRule
}

// NewIoCEngine loads all YAML rule files from a directory (recursive).
func NewIoCEngine(rulesDir string) (*IoCEngine, error) {
	e := &IoCEngine{}
	if rulesDir == "" {
		return e, nil
	}
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
		rules, err := loadIoCRules(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", path, err)
		}
		e.rules = append(e.rules, rules...)
	}
	return e, nil
}

// Scan returns the first matching IoC rule for the event, or ("", "", false).
func (e *IoCEngine) Scan(ev sensor.Event) (ruleID, ttp string, matched bool) {
	for _, rule := range e.rules {
		if matchIoC(rule, ev) {
			return rule.ID, rule.TTP, true
		}
	}
	return "", "", false
}

// Rules returns all loaded rules (for inspection/testing).
func (e *IoCEngine) Rules() []IoCRule { return e.rules }

// matchIoC checks whether an event satisfies a rule.
func matchIoC(rule IoCRule, ev sensor.Event) bool {
	// Event name must match.
	if rule.Event != "" && rule.Event != ev.Name {
		return false
	}

	// Optional process name filter.
	if len(rule.ProcessName) > 0 {
		procName, _ := ev.Args["processName"].(string)
		matched := false
		for _, pn := range rule.ProcessName {
			if strings.EqualFold(pn, procName) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Match each field in rule.Match against event args.
	for key, expected := range rule.Match {
		actual, ok := resolveArg(ev.Args, key)
		if !ok {
			return false
		}
		if !matchValue(expected, actual) {
			return false
		}
	}
	return true
}

// resolveArg resolves a dotted key path like "args.pathname" in the event.
func resolveArg(args map[string]any, key string) (any, bool) {
	// Remove "args." prefix if present.
	key = strings.TrimPrefix(key, "args.")
	v, ok := args[key]
	return v, ok
}

// matchValue checks whether actual matches expected.
// expected can be a string (exact/glob), []any (list of strings), or a number.
func matchValue(expected, actual any) bool {
	switch e := expected.(type) {
	case string:
		a, _ := actual.(string)
		return globMatch(e, a)
	case []any:
		a, _ := actual.(string)
		for _, item := range e {
			s, _ := item.(string)
			if globMatch(s, a) {
				return true
			}
		}
		return false
	case int:
		switch a := actual.(type) {
		case float64:
			return int(a) == e
		case int:
			return a == e
		}
	case float64:
		if a, ok := actual.(float64); ok {
			return a == e
		}
	}
	return false
}

// globMatch supports simple '*' wildcards at start or end.
func globMatch(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return strings.Contains(s, strings.Trim(pattern, "*"))
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(s, strings.TrimPrefix(pattern, "*"))
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(s, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == s
}

func loadIoCRules(path string) ([]IoCRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []IoCRule
	return rules, yaml.Unmarshal(data, &rules)
}
