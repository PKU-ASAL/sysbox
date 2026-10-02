package firecracker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	networkprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Firecracker guests have their own kernel and network namespace. Apply the
// same owned nftables rules used by Docker through the guest's vsock channel.
// The policy target carries the private, persisted VM handle, not a host PID.
type firecrackerPolicyTarget struct {
	ContainerID   string              `json:"container_id"`
	ProviderState json.RawMessage     `json:"provider_state"`
	Bindings      map[string]string   `json:"bindings"`
	AttachmentIPs map[string][]string `json:"attachment_ips"`
	Guest         HandleState         `json:"-"`
}

func decodeFirecrackerPolicyTarget(target driver.PolicyTarget) (firecrackerPolicyTarget, error) {
	var value firecrackerPolicyTarget
	if err := json.Unmarshal(target.State, &value); err != nil {
		return value, driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode policy target", err)
	}
	if value.ContainerID == "" || len(value.ProviderState) == 0 || string(value.ProviderState) == "null" {
		return value, driver.Wrap(driver.ErrorInvalidState, "firecracker", "policy target VM identity is missing", nil)
	}
	if err := json.Unmarshal(value.ProviderState, &value.Guest); err != nil {
		return value, driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode VM policy state", err)
	}
	if value.Guest.VsockUDS == "" {
		return value, driver.Wrap(driver.ErrorInvalidState, "firecracker", "policy requires a guest vsock channel", nil)
	}
	if value.Bindings == nil {
		value.Bindings = map[string]string{}
	}
	return value, nil
}

func (s *Substrate) policyExec(ctx context.Context, target firecrackerPolicyTarget, program string, args ...string) (substrate.ExecResult, error) {
	h := substrate.NodeHandle{ID: target.ContainerID, Provider: &target.Guest}
	result, err := s.ExecInNode(ctx, h, substrate.ExecRequest{Program: program, Args: args, Shell: substrate.ShellNone})
	if err != nil {
		return result, err
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if len(message) > 600 {
			message = message[:600]
		}
		return result, fmt.Errorf("guest command exit %d: %s", result.ExitCode, message)
	}
	return result, nil
}

func nftMissing(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file or directory") || strings.Contains(message, "does not exist")
}

func (s *Substrate) ApplyRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	state, err := decodeFirecrackerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	desired, script, err := networkprovider.CompileRulesetScript(spec, state.Bindings)
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "compile guest ruleset", err)
	}
	// Never replace a table that exists without our ownership marker.
	if _, err := s.ObserveRuleset(ctx, target, spec.Owner); err == nil {
		if _, err := s.policyExec(ctx, state, "/usr/sbin/nft", "delete", "table", "ip", desired.Table); err != nil {
			return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "firecracker", "delete previous guest ruleset", err)
		}
	} else if !driver.IsCategory(err, driver.ErrorNotFound) {
		return driver.RulesetObservation{}, err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	command := "printf '%s' '" + encoded + "' | base64 -d | /usr/sbin/nft -f -"
	if _, err := s.policyExec(ctx, state, "/bin/sh", "-c", command); err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "firecracker", "apply guest ruleset", err)
	}
	observed, err := s.ObserveRuleset(ctx, target, spec.Owner)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	if observed.Digest != desired.Digest {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "guest ruleset digest mismatch", nil)
	}
	return observed, nil
}

func (s *Substrate) ObserveRuleset(ctx context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	state, err := decodeFirecrackerPolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	table := driver.RulesetTableName(owner)
	result, err := s.policyExec(ctx, state, "/usr/sbin/nft", "list", "table", "ip", table)
	if err != nil {
		if nftMissing(err) {
			return driver.RulesetObservation{}, driver.Wrap(driver.ErrorNotFound, "firecracker", "guest ruleset not found", err)
		}
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "firecracker", "read guest ruleset", err)
	}
	return networkprovider.ObserveRulesetText(result.Stdout, owner)
}

func (s *Substrate) DeleteRuleset(ctx context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodeFirecrackerPolicyTarget(target)
	if err != nil {
		return err
	}
	if _, err := s.ObserveRuleset(ctx, target, owner); err != nil {
		if driver.IsCategory(err, driver.ErrorNotFound) {
			return nil
		}
		return err
	}
	if _, err := s.policyExec(ctx, state, "/usr/sbin/nft", "delete", "table", "ip", driver.RulesetTableName(owner)); err != nil {
		return driver.Wrap(driver.ErrorUnavailable, "firecracker", "delete guest ruleset", err)
	}
	return nil
}

var _ driver.Policy = (*Substrate)(nil)
