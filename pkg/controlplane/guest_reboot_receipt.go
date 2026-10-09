package controlplane

import (
	"crypto/ed25519"
	"time"
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
