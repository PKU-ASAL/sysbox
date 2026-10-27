package network

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// GuestPolicyRequest is a bounded built-in operation, never a shell command.
// Node and Identity describe distinct generations: the VM and its policy.
type GuestPolicyRequest struct {
	Operation string                      `json:"operation"`
	Node      substrate.OperationIdentity `json:"node"`
	Identity  substrate.OperationIdentity `json:"identity"`
	Owner     string                      `json:"owner"`
	Spec      driver.RulesetSpec          `json:"spec"`
	Bindings  map[string]string           `json:"bindings,omitempty"`
}

func (r GuestPolicyRequest) Validate() error {
	if r.Node.Validate() != nil || r.Identity.Validate() != nil || r.Node.TopologyUID != r.Identity.TopologyUID || r.Node.OwnerUID != r.Identity.OwnerUID || r.Owner == "" || len(r.Owner) > 1024 {
		return policyEvidenceConflict("guest policy identity required")
	}
	switch r.Operation {
	case "apply", "discover":
		if r.Spec.Owner != r.Owner {
			return policyEvidenceConflict("guest policy owner mismatch")
		}
		_, err := compileRuleset(r.Spec, r.Bindings)
		return err
	case "observe", "delete":
		return nil
	default:
		return policyEvidenceConflict("unknown guest policy operation")
	}
}

// GuestPolicyEngine uses a private persistent directory on the guest root disk.
// Its keys deliberately do not use kernel namespace inodes or boot IDs.
type GuestPolicyEngine struct {
	Root string
	Run  func(context.Context, string, ...string) (string, error)
}
type guestPolicyRecord struct {
	Version  int                         `json:"version"`
	Node     substrate.OperationIdentity `json:"node"`
	Owner    string                      `json:"owner"`
	Evidence policyEvidence              `json:"evidence"`
}

func (e *GuestPolicyEngine) run(ctx context.Context, input string, args ...string) (string, error) {
	if e.Run != nil {
		return e.Run(ctx, input, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/nft", args...)
	cmd.Stdin = strings.NewReader(input)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("guest nft: %w: %.1024s", err, raw)
	}
	return string(raw), nil
}
func (e *GuestPolicyEngine) Execute(ctx context.Context, r GuestPolicyRequest) (driver.RulesetObservation, error) {
	var empty driver.RulesetObservation
	if err := r.Validate(); err != nil {
		return empty, err
	}
	if e.Root == "" {
		return empty, policyEvidenceConflict("guest policy journal required")
	}
	if err := os.MkdirAll(e.Root, 0700); err != nil {
		return empty, err
	}
	info, err := os.Lstat(e.Root)
	if err != nil {
		return empty, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return empty, policyEvidenceConflict("guest policy journal must be a private directory")
	}
	// One lock serializes all operations in this guest, including different
	// owners that accidentally map to the same nft table.
	lock := flock.New(filepath.Join(e.Root, "policy.lock"))
	ok, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !ok {
		lock.Close()
		if err == nil {
			err = ctx.Err()
		}
		return empty, err
	}
	defer lock.Close()
	key := sha256.Sum256([]byte(r.Owner))
	path := filepath.Join(e.Root, fmt.Sprintf("%x.json", key))
	var record *guestPolicyRecord
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return empty, err
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return empty, policyEvidenceConflict("invalid guest policy journal")
		}
		raw, err := io.ReadAll(io.LimitReader(file, 65537))
		if err != nil {
			return empty, err
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			return empty, err
		}
		if record == nil || record.Version != 1 || record.Owner != r.Owner || record.Evidence.validate() != nil || !sameGuestPolicyNode(record.Node, r.Node) {
			return empty, policyEvidenceConflict("guest policy journal ownership mismatch")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return empty, err
	}
	var evidence *policyEvidence
	if record != nil {
		evidence = &record.Evidence
	}
	if err = evidence.checkIdentity(r.Identity); err != nil {
		return empty, err
	}
	output, err := e.run(ctx, "", "list", "ruleset")
	if err != nil {
		return empty, err
	}
	current, inspectErr := observeFromNFTList(output, r.Owner)
	absent := driver.IsCategory(inspectErr, driver.ErrorNotFound)
	if inspectErr != nil && !absent {
		return empty, inspectErr
	}
	marker := ""
	if !absent {
		block, _ := nftTableBlock(output, current.Table)
		matches := policyMarkerRE.FindAllStringSubmatch(block, -1)
		if len(matches) != 1 {
			return empty, policyEvidenceConflict("guest policy marker missing")
		}
		marker = matches[0][1]
	}
	save := func(phase, digest string) error {
		v := guestPolicyRecord{Version: 1, Node: r.Node, Owner: r.Owner, Evidence: policyEvidence{Version: 1, Identity: r.Identity, Digest: digest, Phase: phase}}
		return saveGuestPolicyRecord(e.Root, path, v)
	}
	switch r.Operation {
	case "observe", "discover":
		// Finalized deletion proves absence but still fences same-generation apply.
		// An interrupted deletion remains unknown until an authorized retry settles it.
		if absent && (evidence == nil || evidence.Phase != "deleting") {
			return empty, inspectErr
		}
		if evidence != nil && (evidence.Identity.Generation != r.Identity.Generation || evidence.Phase == "deleted" || evidence.Phase == "deleting") {
			return empty, policyEvidenceConflict("guest policy generation retired or unsettled")
		}
		if absent {
			return empty, inspectErr
		}
		if marker != policyIdentityMarker(r.Identity) {
			return empty, policyEvidenceConflict("guest policy generation mismatch")
		}
		if r.Operation == "discover" {
			plan, err := compileRuleset(r.Spec, r.Bindings)
			if err != nil {
				return empty, err
			}
			if plan.Digest != current.Digest {
				return empty, policyEvidenceConflict("guest policy digest mismatch")
			}
		}
		return current, nil
	case "delete":
		if evidence != nil && evidence.Identity.Generation != r.Identity.Generation {
			return empty, policyEvidenceConflict("delete requires exact guest policy generation")
		}
		if !absent && marker != policyIdentityMarker(r.Identity) {
			return empty, policyEvidenceConflict("delete target generation mismatch")
		}
		if err = save("deleting", ""); err != nil {
			return empty, err
		}
		if !absent {
			if _, err = e.run(ctx, "delete table ip "+current.Table+"\n", "-f", "-"); err != nil {
				return empty, err
			}
		}
		return empty, save("deleted", "")
	case "apply":
		plan, err := compileRuleset(r.Spec, r.Bindings)
		if err != nil {
			return empty, err
		}
		if err = evidence.checkApply(r.Identity, plan.Digest); err != nil {
			return empty, err
		}
		if !absent {
			retry, err := validatePolicyTransition(marker, current.Digest, r.Identity, plan.Digest)
			if err != nil {
				return empty, err
			}
			if retry {
				return current, save("active", plan.Digest)
			}
		}
		if err = save("prepared", plan.Digest); err != nil {
			return empty, err
		}
		plan.IdentityMarker = policyIdentityMarker(r.Identity)
		script := nftScript(plan)
		if !absent {
			script = "delete table ip " + plan.Table + "\n" + script
		}
		if _, err = e.run(ctx, script, "-f", "-"); err != nil {
			return empty, err
		}
		out, err := e.run(ctx, "", "list", "ruleset")
		if err != nil {
			return empty, err
		}
		observed, err := observeFromNFTList(out, r.Owner)
		if err != nil {
			return empty, err
		}
		if observed.Digest != plan.Digest {
			return empty, policyEvidenceConflict("guest policy readback mismatch")
		}
		return observed, save("active", plan.Digest)
	}
	return empty, policyEvidenceConflict("unknown operation")
}
func sameGuestPolicyNode(a, b substrate.OperationIdentity) bool {
	return a.OwnerUID == b.OwnerUID && a.TopologyUID == b.TopologyUID && a.ResourceUID == b.ResourceUID && a.Generation == b.Generation
}
func saveGuestPolicyRecord(root, path string, r guestPolicyRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".policy-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(root)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
