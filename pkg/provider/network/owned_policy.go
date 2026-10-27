package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
)

const ownershipPrefix = "sysbox-owner="
const digestMarkerPrefix = "sysbox-digest="
const maxNFTCommentLength = 128

// netnsTimeout bounds each nsenter/nft subprocess so a wedged netfilter
// operation cannot hang the caller; the subprocess is killed on deadline.
const netnsTimeout = 15 * time.Second

type policyTargetState struct {
	Namespace string            `json:"namespace"`
	Bindings  map[string]string `json:"bindings"`
}

func (Driver) ApplyRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	ns, err := os.Open("/var/run/netns/" + state.Namespace)
	if err != nil {
		return driver.RulesetObservation{}, networkQueryError("open policy namespace", err)
	}
	defer ns.Close()
	return ApplyManagedRuleset(ctx, ns, target.Policy, spec, state.Bindings)
}

// ObserveRuleset reads the ruleset back inside the target netns via an nsenter
// subprocess (killable), returning the observed digest.
func (Driver) ObserveRuleset(ctx context.Context, target driver.PolicyTarget, owner string) (driver.RulesetObservation, error) {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	ns, err := os.Open("/var/run/netns/" + state.Namespace)
	if err != nil {
		return driver.RulesetObservation{}, networkQueryError("open policy namespace", err)
	}
	defer ns.Close()
	return ObserveManagedRuleset(ctx, ns, target.Policy, owner)
}

func (Driver) DeleteRuleset(ctx context.Context, target driver.PolicyTarget, owner string) error {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return err
	}
	ns, err := os.Open("/var/run/netns/" + state.Namespace)
	if err != nil {
		return networkQueryError("open policy namespace", err)
	}
	defer ns.Close()
	return DeleteManagedRuleset(ctx, ns, target.Policy, owner)
}

func (Driver) DiscoverRuleset(ctx context.Context, target driver.PolicyTarget, spec driver.RulesetSpec) (driver.RulesetObservation, error) {
	state, err := decodePolicyTarget(target)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	ns, err := os.Open("/var/run/netns/" + state.Namespace)
	if err != nil {
		return driver.RulesetObservation{}, networkQueryError("open policy namespace", err)
	}
	defer ns.Close()
	return DiscoverManagedRuleset(ctx, ns, target.Policy, spec, state.Bindings)
}

func decodePolicyTarget(target driver.PolicyTarget) (policyTargetState, error) {
	var state policyTargetState
	if err := target.Validate(); err != nil {
		return state, err
	}
	if err := json.Unmarshal(target.State, &state); err != nil {
		return state, driver.Wrap(driver.ErrorInvalidState, "network", "decode policy target", err)
	}
	if state.Namespace == "" {
		return state, driver.Wrap(driver.ErrorInvalidState, "network", "policy namespace is required", nil)
	}
	if state.Bindings == nil {
		state.Bindings = map[string]string{}
	}
	return state, nil
}

// ApplyRulesetInNetNS borrows an open namespace file for the complete apply.
// The caller retains ownership and must keep it open until this call returns.
func ApplyRulesetInNetNS(ctx context.Context, ns *os.File, spec driver.RulesetSpec, bindings map[string]string) (driver.RulesetObservation, error) {
	plan, err := compileRuleset(spec, bindings)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	if err := applyCompiledInNetNS(ctx, ns, plan); err != nil {
		return driver.RulesetObservation{}, err
	}
	return driver.RulesetObservation{Table: plan.Table, Digest: plan.Digest}, nil
}

// CompileRulesetScript shares Docker's tested nftables rule compiler with
// guests whose network namespace is not visible to the host (microVMs).
func CompileRulesetScript(spec driver.RulesetSpec, bindings map[string]string) (driver.RulesetObservation, string, error) {
	plan, err := compileRuleset(spec, bindings)
	if err != nil {
		return driver.RulesetObservation{}, "", err
	}
	return driver.RulesetObservation{Table: plan.Table, Digest: plan.Digest}, nftScript(plan), nil
}

// ObserveRulesetText verifies owned markers from a guest's nft list ruleset.
func ObserveRulesetText(output, owner string) (driver.RulesetObservation, error) {
	return observeFromNFTList(output, owner)
}

// ObserveRulesetInNetNS borrows an open namespace file for the bounded query.
func ObserveRulesetInNetNS(ctx context.Context, ns *os.File, owner string) (driver.RulesetObservation, error) {
	out, err := runNFT(ctx, ns, "", "list", "ruleset")
	if err != nil {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorUnavailable, "network", "list nftables ruleset", err)
	}
	return observeFromNFTList(out, owner)
}

// DeleteRulesetInNetNS borrows an open namespace file to remove the owned table.
// Only a missing table is treated as success; other failures are returned.
func DeleteRulesetInNetNS(ctx context.Context, ns *os.File, owner string) error {
	_, err := runNFT(ctx, ns, "", "delete", "table", "ip", driver.RulesetTableName(owner))
	if err != nil && !isNFTNotFound(err) {
		return driver.Wrap(driver.ErrorUnavailable, "network", "delete nftables ruleset", err)
	}
	return nil
}

func applyCompiledInNetNS(ctx context.Context, ns *os.File, plan compiledRuleset) error {
	// nft add is idempotent for tables. Ensure existence, then replace within
	// one transaction so any failure preserves the entire previous ruleset.
	script := fmt.Sprintf("add table ip %s\ndelete table ip %s\n", plan.Table, plan.Table) + nftScript(plan)
	_, err := runNFT(ctx, ns, script, "-f", "-")
	return err
}

// runNFT runs `nft <args...>` inside the held namespace via the sysbox-netns
// helper (a shim over nsenter). The helper runs as a killable
// subprocess, so a wedged netfilter operation can no longer hang the caller the
// way an in-process netlink read did. stdin, when non-empty, is piped through
// to `nft` (used for `-f -`).
func runNFT(ctx context.Context, ns *os.File, stdin string, args ...string) (string, error) {
	if ns == nil {
		return "", fmt.Errorf("policy namespace file is required")
	}
	if _, err := ns.Stat(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, netnsTimeout)
	defer cancel()
	full := append([]string{"/proc/self/fd/3"}, args...)
	cmd := exec.CommandContext(ctx, "sysbox-netns", full...)
	cmd.ExtraFiles = []*os.File{ns}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), &nftCommandError{err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return string(out), nil
}

type nftCommandError struct {
	err    error
	stderr string
}

func (e *nftCommandError) Error() string {
	if e.stderr == "" {
		return e.err.Error()
	}
	return fmt.Sprintf("%v: %s", e.err, e.stderr)
}

func (e *nftCommandError) Unwrap() error { return e.err }

func isNFTNotFound(err error) bool {
	var commandErr *nftCommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	message := strings.ToLower(commandErr.stderr)
	if strings.Contains(message, "nsenter:") {
		return false
	}
	return strings.Contains(message, "no such file or directory") || strings.Contains(message, "does not exist")
}

func ownershipMarker(owner, digest string) string {
	marker := ownershipPrefix + owner + ";digest=" + digest
	if len(marker) <= maxNFTCommentLength {
		return marker
	}
	return digestMarkerPrefix + digest
}

var ownerMarkerRE = regexp.MustCompile(`comment "sysbox-owner=([^;"]+);digest=([^;"]+)`)
var digestMarkerRE = regexp.MustCompile(`comment "sysbox-digest=([^"]+)`)

// observeFromNFTList parses `nft list ruleset` output and extracts the ownership
// marker digest. Every rule carrying a marker must agree on owner and digest,
// matching the write-side contract that the owner marker is the source of truth.
func observeFromNFTList(output, owner string) (driver.RulesetObservation, error) {
	tableName := driver.RulesetTableName(owner)
	tableBlock, ok := nftTableBlock(output, tableName)
	if !ok {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorNotFound, "network", "owned ruleset not found", nil)
	}
	ownerMatches := ownerMarkerRE.FindAllStringSubmatch(tableBlock, -1)
	digestMatches := digestMarkerRE.FindAllStringSubmatch(tableBlock, -1)
	if len(ownerMatches) == 0 && len(digestMatches) == 0 {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "table exists without matching ownership marker", nil)
	}
	digest := ""
	for _, m := range ownerMatches {
		if m[1] != owner {
			return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "owned table contains a rule without matching ownership marker", nil)
		}
		if digest != "" && digest != m[2] {
			return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "owned table contains inconsistent policy digests", nil)
		}
		digest = m[2]
	}
	for _, m := range digestMatches {
		if digest != "" && digest != m[1] {
			return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "owned table contains inconsistent policy digests", nil)
		}
		digest = m[1]
	}
	return driver.RulesetObservation{Table: tableName, Digest: digest}, nil
}

func nftTableBlock(output, tableName string) (string, bool) {
	startRE := regexp.MustCompile(`(?m)^table ip ` + regexp.QuoteMeta(tableName) + ` \{`)
	loc := startRE.FindStringIndex(output)
	if loc == nil {
		return "", false
	}
	depth := 0
	quoted, escaped := false, false
	for i := loc[0]; i < len(output); i++ {
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if output[i] == '\\' {
				escaped = true
				continue
			}
			if output[i] == '"' {
				quoted = false
			}
			continue
		}
		if output[i] == '"' {
			quoted = true
			continue
		}
		switch output[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return output[loc[0] : i+1], true
			}
		}
	}
	// A header was found, so malformed/truncated content is a conflict, never
	// proof of absence authorizing a new generation to overwrite the table.
	return "", true
}
