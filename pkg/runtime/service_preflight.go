package runtime

import (
	"fmt"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

type PreflightCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"` // error | warning | info
	Message  string `json:"message,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type PreflightResult struct {
	OK     bool             `json:"ok"`
	Checks []PreflightCheck `json:"checks"`
}

func (r *PreflightResult) add(name string, ok bool, severity, message, hint string) {
	r.Checks = append(r.Checks, PreflightCheck{
		Name:     name,
		OK:       ok,
		Severity: severity,
		Message:  message,
		Hint:     hint,
	})
	if !ok && severity == "error" {
		r.OK = false
	}
}

func (r *PreflightResult) Err() error {
	if r.OK {
		return nil
	}
	for _, c := range r.Checks {
		if !c.OK && c.Severity == "error" {
			if c.Hint != "" {
				return fmt.Errorf("preflight failed: %s: %s (%s)", c.Name, c.Message, c.Hint)
			}
			return fmt.Errorf("preflight failed: %s: %s", c.Name, c.Message)
		}
	}
	return fmt.Errorf("preflight failed")
}

func PreflightFile(hclPath string, registry *driver.Registry) (*PreflightResult, error) {
	root, err := config.ParseFile(hclPath)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	ctx, err := config.BuildEvalContext(root, filepath.Dir(hclPath))
	if err != nil {
		return nil, fmt.Errorf("evaluate config: %w", err)
	}
	res := &PreflightResult{OK: true}

	needed := map[string]bool{}
	for _, sb := range root.Substrates {
		needed[sb.Type] = true
	}

	for name := range needed {
		if nodeDriver, err := registry.RequireNode(name); err == nil {
			AddPreflightChecks(res, nodeDriver.PreflightChecks(true))
		}
	}

	for _, rb := range root.Resources {
		AddPreflightChecks(res, ResourcePreflightChecks(rb, ctx))
	}

	return res, nil
}

func AddPreflightChecks(res *PreflightResult, checks []substrate.PreflightCheck) {
	for _, c := range checks {
		res.add(c.Name, c.OK, c.Severity, c.Message, c.Hint)
	}
}
