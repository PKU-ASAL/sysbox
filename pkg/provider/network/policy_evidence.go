package network

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Resource-side write-ahead evidence; it does not replace the control-plane
// action journal. Access is serialized by the same namespace lock as nft.
type policyEvidence struct {
	Version  int                         `json:"version"`
	Identity substrate.OperationIdentity `json:"identity"`
	Digest   string                      `json:"digest"`
	Phase    string                      `json:"phase"`
}

type policyEvidenceStore struct{ root string }

func (r policyEvidence) validate() error {
	if r.Version != 1 || r.Identity.Validate() != nil {
		return policyEvidenceConflict("invalid policy evidence identity/version")
	}
	switch r.Phase {
	case "prepared", "active":
		if r.Digest == "" {
			return policyEvidenceConflict("missing policy digest")
		}
	case "deleting", "deleted":
	default:
		return policyEvidenceConflict("invalid policy evidence phase")
	}
	return nil
}

func policyEvidenceConflict(reason string) error {
	return driver.Wrap(driver.ErrorInvalidState, "network", reason, nil)
}

func (r *policyEvidence) checkIdentity(id substrate.OperationIdentity) error {
	if r == nil {
		return nil
	}
	if err := r.validate(); err != nil {
		return err
	}
	if r.Identity.OwnerUID != id.OwnerUID || r.Identity.TopologyUID != id.TopologyUID || r.Identity.ResourceUID != id.ResourceUID || id.Generation < r.Identity.Generation {
		return policyEvidenceConflict("policy durable generation/ownership conflict")
	}
	return nil
}

func (r *policyEvidence) checkApply(id substrate.OperationIdentity, digest string) error {
	if err := r.checkIdentity(id); err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	if r.Phase == "deleting" {
		return policyEvidenceConflict("policy deletion must finish before apply")
	}
	if id.Generation == r.Identity.Generation && (r.Phase == "deleted" || r.Digest != digest) {
		return policyEvidenceConflict("retired or immutable policy generation")
	}
	return nil
}

func (s policyEvidenceStore) path(ns *os.File, owner string) (string, error) {
	if ns == nil {
		return "", fmt.Errorf("policy namespace required")
	}
	info, err := ns.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("namespace inode unavailable")
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", stat.Dev, stat.Ino, owner)))
	return filepath.Join(s.root, fmt.Sprintf("%x.json", key)), nil
}

func (s policyEvidenceStore) load(ns *os.File, owner string) (*policyEvidence, error) {
	path, err := s.path(ns, owner)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, policyEvidenceConflict("unbounded/nonregular policy evidence")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, policyEvidenceConflict("oversized policy evidence")
	}
	var record policyEvidence
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, policyEvidenceConflict("corrupt policy evidence")
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return &record, nil
}

func (s policyEvidenceStore) save(ns *os.File, owner string, record policyEvidence) error {
	if err := record.validate(); err != nil {
		return err
	}
	path, err := s.path(ns, owner)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.root, ".policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
