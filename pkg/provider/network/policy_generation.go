package network

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func policyIdentityMarker(id substrate.OperationIdentity) string {
	sum := sha256.Sum256([]byte(id.OwnerUID + "\x00" + id.TopologyUID + "\x00" + id.ResourceUID))
	return fmt.Sprintf("sysbox-policy=%x;generation=%d", sum, id.Generation)
}

func validatePolicyTransition(marker, currentDigest string, id substrate.OperationIdentity, desiredDigest string) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	parts := strings.Split(marker, ";generation=")
	if len(parts) != 2 || parts[0] != strings.Split(policyIdentityMarker(id), ";")[0] {
		return false, driver.Wrap(driver.ErrorInvalidState, "network", "policy ownership conflict", nil)
	}
	current, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || current < 1 || current > id.Generation || current == id.Generation && currentDigest != desiredDigest {
		return false, driver.Wrap(driver.ErrorInvalidState, "network", "policy generation or immutable digest conflict", err)
	}
	return current == id.Generation, nil
}

// Every managed reader/writer uses the same host-local namespace lock. Keep
// lock files: unlinking one would let a waiter and a new caller lock separately.
func lockPolicyNamespace(ctx context.Context, ns *os.File) (func(), error) {
	return lockPolicyNamespaceIn(ctx, ns, "/var/lib/sysbox/policy-locks")
}

func lockPolicyNamespaceIn(ctx context.Context, ns *os.File, root string) (func(), error) {
	if ns == nil {
		return nil, fmt.Errorf("policy namespace required")
	}
	info, err := ns.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("namespace inode unavailable")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, fmt.Sprintf("%d-%d.lock", stat.Dev, stat.Ino)), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

var policyMarkerRE = regexp.MustCompile(`(?m)^\s*comment "(sysbox-policy=[a-f0-9]{64};generation=[0-9]+)"\s*$`)

func inspectManagedPolicy(ctx context.Context, ns *os.File, owner string) (driver.RulesetObservation, string, error) {
	out, err := runNFT(ctx, ns, "", "list", "ruleset")
	if err != nil {
		return driver.RulesetObservation{}, "", driver.Wrap(driver.ErrorUnavailable, "network", "inspect managed policy", err)
	}
	observed, err := observeFromNFTList(out, owner)
	if err != nil {
		return observed, "", err
	}
	block, _ := nftTableBlock(out, driver.RulesetTableName(owner))
	matches := policyMarkerRE.FindAllStringSubmatch(block, -1)
	if len(matches) != 1 {
		return observed, "", driver.Wrap(driver.ErrorInvalidState, "network", "policy generation evidence missing or ambiguous", nil)
	}
	return observed, matches[0][1], nil
}

func ApplyManagedRuleset(ctx context.Context, ns *os.File, id substrate.OperationIdentity, spec driver.RulesetSpec, bindings map[string]string) (driver.RulesetObservation, error) {
	if err := id.Validate(); err != nil {
		return driver.RulesetObservation{}, err
	}
	plan, err := compileRuleset(spec, bindings)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	unlock, err := lockPolicyNamespace(ctx, ns)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	defer unlock()
	store := policyEvidenceStore{root: "/var/lib/sysbox/policy-evidence"}
	evidence, err := store.load(ns, spec.Owner)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	if err := evidence.checkApply(id, plan.Digest); err != nil {
		return driver.RulesetObservation{}, err
	}
	current, marker, err := inspectManagedPolicy(ctx, ns, spec.Owner)
	if err != nil && !driver.IsCategory(err, driver.ErrorNotFound) {
		return driver.RulesetObservation{}, err
	}
	if err == nil {
		retry, err := validatePolicyTransition(marker, current.Digest, id, plan.Digest)
		if err != nil {
			return driver.RulesetObservation{}, err
		}
		if retry {
			if err := store.save(ns, spec.Owner, policyEvidence{Version: 1, Identity: id, Digest: plan.Digest, Phase: "active"}); err != nil {
				return driver.RulesetObservation{}, err
			}
			return current, nil
		}
	}
	plan.IdentityMarker = policyIdentityMarker(id)
	intent := policyEvidence{Version: 1, Identity: id, Digest: plan.Digest, Phase: "prepared"}
	if err := store.save(ns, spec.Owner, intent); err != nil {
		return driver.RulesetObservation{}, err
	}
	if err := applyCompiledInNetNS(ctx, ns, plan); err != nil {
		return driver.RulesetObservation{}, err
	}
	intent.Phase = "active"
	if err := store.save(ns, spec.Owner, intent); err != nil {
		return driver.RulesetObservation{}, err
	}
	return driver.RulesetObservation{Table: plan.Table, Digest: plan.Digest}, nil
}

func ObserveManagedRuleset(ctx context.Context, ns *os.File, id substrate.OperationIdentity, owner string) (driver.RulesetObservation, error) {
	if err := id.Validate(); err != nil {
		return driver.RulesetObservation{}, err
	}
	unlock, err := lockPolicyNamespace(ctx, ns)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	defer unlock()
	evidence, err := (policyEvidenceStore{root: "/var/lib/sysbox/policy-evidence"}).load(ns, owner)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	if err := evidence.checkIdentity(id); err != nil {
		return driver.RulesetObservation{}, err
	}
	observed, marker, err := inspectManagedPolicy(ctx, ns, owner)
	if err == nil && marker != policyIdentityMarker(id) {
		err = driver.Wrap(driver.ErrorInvalidState, "network", "policy generation conflict", nil)
	}
	return observed, err
}

func DiscoverManagedRuleset(ctx context.Context, ns *os.File, id substrate.OperationIdentity, spec driver.RulesetSpec, bindings map[string]string) (driver.RulesetObservation, error) {
	plan, err := compileRuleset(spec, bindings)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	observed, err := ObserveManagedRuleset(ctx, ns, id, spec.Owner)
	if err != nil {
		return driver.RulesetObservation{}, err
	}
	if observed.Table != plan.Table || observed.Digest != plan.Digest {
		return driver.RulesetObservation{}, driver.Wrap(driver.ErrorInvalidState, "network", "discovered policy differs from desired ruleset", nil)
	}
	return observed, nil
}

func DeleteManagedRuleset(ctx context.Context, ns *os.File, id substrate.OperationIdentity, owner string) error {
	if err := id.Validate(); err != nil {
		return err
	}
	unlock, err := lockPolicyNamespace(ctx, ns)
	if err != nil {
		return err
	}
	defer unlock()
	store := policyEvidenceStore{root: "/var/lib/sysbox/policy-evidence"}
	evidence, err := store.load(ns, owner)
	if err != nil {
		return err
	}
	if err := evidence.checkIdentity(id); err != nil {
		return err
	}
	if evidence != nil && evidence.Identity.Generation != id.Generation {
		return policyEvidenceConflict("delete requires exact durable policy generation")
	}
	_, marker, err := inspectManagedPolicy(ctx, ns, owner)
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return store.save(ns, owner, policyEvidence{Version: 1, Identity: id, Phase: "deleted"})
	}
	if err != nil {
		return err
	}
	if marker != policyIdentityMarker(id) {
		return driver.Wrap(driver.ErrorInvalidState, "network", "policy generation conflict", nil)
	}
	intent := policyEvidence{Version: 1, Identity: id, Phase: "deleting"}
	if err := store.save(ns, owner, intent); err != nil {
		return err
	}
	if err := DeleteRulesetInNetNS(ctx, ns, owner); err != nil {
		return err
	}
	intent.Phase = "deleted"
	return store.save(ns, owner, intent)
}
