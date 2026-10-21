package controlplane

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// HostFenceRequest is an admitted evidence request. It is not a stop receipt,
// a VM power credential, or authority to release a topology after a reboot.
type HostFenceRequest struct {
	Version       int            `json:"version"`
	ID            string         `json:"id"`
	Topology      string         `json:"topology"`
	RunID         string         `json:"run_id"`
	LabID         string         `json:"lab_id"`
	ControllerKey []byte         `json:"controller_key"`
	JournalSHA256 string         `json:"journal_sha256"`
	Grant         ExecutionGrant `json:"grant"`
}

var fenceName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func validFenceDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == s
}
func validFenceBoot(s string) bool {
	v, err := uuid.Parse(s)
	return err == nil && v != uuid.Nil && v.String() == s
}
func (r HostFenceRequest) Validate() error {
	g := r.Grant
	runScope := IsRunExecutionOperation(g.Operation) && g.Scope.ResourceUID == "" && g.Scope.Generation == 0
	adoptionScope := g.Operation == "adoption_apply" && g.Scope.ResourceUID != "" && g.Scope.Generation > 0
	gcScope := g.Operation == "gc_delete" && g.Scope.ResourceUID != "" && g.Scope.Generation > 0 && g.CommandID == r.RunID
	if r.Version != 1 || !fenceName.MatchString(r.ID) || !fenceName.MatchString(r.Topology) || !fenceName.MatchString(r.RunID) || !fenceName.MatchString(r.LabID) || len(r.ControllerKey) != ed25519.PublicKeySize || !validFenceDigest(r.JournalSHA256) || !validFenceDigest(g.Digest) || !validFenceBoot(g.TargetBootID) || g.Scope.TopologyUID == "" || g.ActionID != r.RunID || g.AgentID == "" || g.SessionID == "" || g.CommandID == "" || g.Epoch == 0 || g.Attempt == 0 || g.ParentEpoch != 0 || g.ExpiresAt.IsZero() || g.StopReceipt != "" || (g.State != GrantActive && g.State != GrantRevoked) || (!runScope && !gcScope && !adoptionScope) {
		return fmt.Errorf("invalid host fence request")
	}
	return nil
}
func (r HostFenceRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("sysbox.host-fence-request.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

type HostFenceReceipt struct {
	Version                 int       `json:"version"`
	RequestSHA256           string    `json:"request_sha256"`
	LabID                   string    `json:"lab_id"`
	OldBootID               string    `json:"old_boot_id"`
	NewBootID               string    `json:"new_boot_id"`
	ControllerBootID        string    `json:"controller_boot_id"`
	ControllerBootUnchanged bool      `json:"controller_boot_unchanged"`
	QEMUPID                 int       `json:"qemu_pid"`
	QEMUStart               string    `json:"qemu_start"`
	QEMUExited              bool      `json:"qemu_exited"`
	DiskDevice              uint64    `json:"disk_device"`
	DiskInode               uint64    `json:"disk_inode"`
	ProviderScope           string    `json:"provider_scope"`
	OperationResult         string    `json:"operation_result"`
	ObservedAt              time.Time `json:"observed_at"`
	Signature               []byte    `json:"signature"`
}

func (v HostFenceReceipt) validate(r HostFenceRequest) error {
	digest, err := r.Digest()
	if err != nil {
		return err
	}
	start, parseErr := strconv.ParseUint(v.QEMUStart, 10, 64)
	if v.Version != 1 || v.RequestSHA256 != digest || v.LabID != r.LabID || v.OldBootID != r.Grant.TargetBootID || !validFenceBoot(v.NewBootID) || v.NewBootID == v.OldBootID || !validFenceBoot(v.ControllerBootID) || v.ControllerBootID == v.OldBootID || v.ControllerBootID == v.NewBootID || !v.ControllerBootUnchanged || v.QEMUPID <= 1 || parseErr != nil || start == 0 || !v.QEMUExited || v.DiskDevice == 0 || v.DiskInode == 0 || v.ProviderScope != "local-only" || v.OperationResult != "unknown" || v.ObservedAt.IsZero() {
		return fmt.Errorf("host fence receipt does not prove scoped process exclusion")
	}
	return nil
}
func fenceReceiptMessage(v HostFenceReceipt) ([]byte, error) {
	v.Signature = nil
	raw, err := json.Marshal(v)
	return append([]byte("sysbox.host-fence-receipt.v1\x00"), raw...), err
}
func SignHostFenceReceipt(key ed25519.PrivateKey, r HostFenceRequest, v HostFenceReceipt) (HostFenceReceipt, error) {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) || !bytes.Equal(key.Public().(ed25519.PublicKey), r.ControllerKey) {
		return HostFenceReceipt{}, fmt.Errorf("fence controller key mismatch")
	}
	if err := v.validate(r); err != nil {
		return HostFenceReceipt{}, err
	}
	message, err := fenceReceiptMessage(v)
	if err != nil {
		return HostFenceReceipt{}, err
	}
	v.Signature = ed25519.Sign(key, message)
	return v, nil
}
func VerifyHostFenceReceipt(r HostFenceRequest, v HostFenceReceipt) error {
	if err := v.validate(r); err != nil {
		return err
	}
	message, err := fenceReceiptMessage(v)
	if err != nil {
		return err
	}
	if !ed25519.Verify(r.ControllerKey, message, v.Signature) {
		return fmt.Errorf("invalid fence controller signature")
	}
	return nil
}
