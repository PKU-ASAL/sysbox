package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/substrate"
	"time"
)

// SignedExecutionStopReceipt is produced only by the resource-host gate after
// it has proved that the grant cannot create further effects. Its signing key
// is distinct from both worker authentication and API issuer credentials.
type SignedExecutionStopReceipt struct {
	EvidenceDigest string                       `json:"evidence_digest,omitempty"`
	InstallID      string                       `json:"install_id,omitempty"`
	InstallDigest  string                       `json:"install_digest,omitempty"`
	FileResult     *substrate.FileInstallResult `json:"file_result,omitempty"`
	GuestResult    *GuestTaskResult             `json:"guest_result,omitempty"`
	GuestBootID    string                       `json:"guest_boot_id,omitempty"`
	TaskID         string                       `json:"task_id,omitempty"`
	TaskDigest     string                       `json:"task_digest,omitempty"`
	Quiescence     string                       `json:"quiescence"`
	Grant          ExecutionGrant               `json:"grant"`
	HostBootID     string                       `json:"host_boot_id"`
	ReceiptID      string                       `json:"receipt_id"`
	StoppedAt      time.Time                    `json:"stopped_at"`
	Signature      []byte                       `json:"signature"`
}

func stopReceiptMessage(r SignedExecutionStopReceipt) ([]byte, error) {
	r.Signature = nil
	raw, err := json.Marshal(r)
	return append([]byte("sysbox.execution-stop.v1\x00"), raw...), err
}
func validStopReceipt(r SignedExecutionStopReceipt) bool {
	g := r.Grant
	if (g.Operation == "adoption_apply" || g.Operation == "ssh_access_apply" || g.Operation == "ssh_access_delete") && (len(r.EvidenceDigest) != 64 || g.Scope.ResourceUID == "") {
		return false
	}
	if g.Operation == "guest_file_install" {
		if r.GuestBootID == "" || r.GuestBootID != g.TargetBootID || r.InstallID != g.ActionID || r.InstallDigest != g.Digest || r.FileResult == nil || !r.FileResult.Quiescent || (r.FileResult.Outcome != substrate.FileInstallCommitted && r.FileResult.Outcome != substrate.FileInstallCancelled) {
			return false
		}
	}
	if r.Quiescence != "confirmed" {
		return false
	}
	if (g.Scope.ResourceUID == "" && g.Scope.Generation != 0) || (g.Scope.ResourceUID != "" && g.Scope.Generation < 1) {
		return false
	}
	if g.Operation == "guest_task" && (r.GuestBootID == "" || g.TargetBootID != r.GuestBootID || r.TaskID != g.ActionID || r.TaskDigest != g.Digest) {
		return false
	}
	if g.Operation == "guest_task" && (r.GuestResult == nil || r.GuestResult.Validate() != nil) {
		return false
	}
	if g.Operation == "guest_task" && g.Scope.ResourceUID == "" {
		return false
	}
	return r.HostBootID != "" && r.ReceiptID != "" && !r.StoppedAt.IsZero() && g.Scope.TopologyUID != "" && g.AgentID != "" && g.SessionID != "" && g.ActionID != "" && g.CommandID != "" && g.Attempt > 0 && g.Epoch > 0 && g.Operation != "" && g.Digest != ""
}

func SignExecutionFileStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, hostBootID, guestBootID, id, digest string, result substrate.FileInstallResult, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "guest_file_install" {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: hostBootID, GuestBootID: guestBootID, InstallID: id, InstallDigest: digest, FileResult: &result, ReceiptID: receiptID, StoppedAt: now.UTC(), Quiescence: "confirmed"})
}
func SignExecutionStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, bootID, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	r := SignedExecutionStopReceipt{Grant: g, HostBootID: bootID, ReceiptID: receiptID, StoppedAt: now.UTC(), Quiescence: "confirmed"}
	return signStopReceipt(key, r)
}

func SignExecutionTaskStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, hostBootID, guestBootID, taskID, taskDigest string, result GuestTaskResult, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "guest_task" {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, GuestResult: &result, HostBootID: hostBootID, GuestBootID: guestBootID, TaskID: taskID, TaskDigest: taskDigest, ReceiptID: receiptID, StoppedAt: now.UTC(), Quiescence: "confirmed"})
}
func signStopReceipt(key ed25519.PrivateKey, r SignedExecutionStopReceipt) (SignedExecutionStopReceipt, error) {
	if len(key) != ed25519.PrivateKeySize || !validStopReceipt(r) {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	raw, err := stopReceiptMessage(r)
	if err != nil {
		return SignedExecutionStopReceipt{}, err
	}
	r.Signature = ed25519.Sign(key, raw)
	return r, nil
}
func VerifyExecutionStopReceipt(key ed25519.PublicKey, r SignedExecutionStopReceipt) error {
	if len(key) != ed25519.PublicKeySize || !validStopReceipt(r) {
		return ErrInvalidExecutionGrant
	}
	raw, err := stopReceiptMessage(r)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, r.Signature) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
