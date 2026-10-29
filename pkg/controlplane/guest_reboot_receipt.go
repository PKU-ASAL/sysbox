package controlplane

import (
	"crypto/ed25519"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// The resource host verifies canonical provider UID/generation before reading
// the new guest boot. This proves old-boot quiescence, never its exit result.
func SignRebootedTaskStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, hostBoot, currentGuestBoot, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "guest_task" || currentGuestBoot == "" || currentGuestBoot == g.TargetBootID {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	result := GuestTaskResult{Result: GuestExecutionResult{Encoding: "base64"}, ResultClass: "unknown"}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, GuestResult: &result, HostBootID: hostBoot, GuestBootID: g.TargetBootID, ObservedGuestBootID: currentGuestBoot, TaskID: g.ActionID, TaskDigest: g.Digest, ReceiptID: receiptID, StoppedAt: now.UTC(), Quiescence: "confirmed"})
}

func SignRebootedFileStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, hostBoot, currentGuestBoot, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	if g.Operation != "guest_file_install" || currentGuestBoot == "" || currentGuestBoot == g.TargetBootID {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	result := substrate.FileInstallResult{Outcome: substrate.FileInstallUnknown, Quiescent: true}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, FileResult: &result, HostBootID: hostBoot, GuestBootID: g.TargetBootID, ObservedGuestBootID: currentGuestBoot, InstallID: g.ActionID, InstallDigest: g.Digest, ReceiptID: receiptID, StoppedAt: now.UTC(), Quiescence: "confirmed"})
}
