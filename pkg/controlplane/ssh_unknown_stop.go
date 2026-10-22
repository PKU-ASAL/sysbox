package controlplane

import (
	"crypto/ed25519"
	"github.com/oslab/sysbox/pkg/substrate"
	"time"
)

func validSSHUnknownStop(g ExecutionGrant, boot string, stop *substrate.SSHAccessStop) bool {
	if stop == nil || !IsGuestSSHAccessOperation(g.Operation) || g.ParentEpoch == 0 || stop.Outcome != "unknown" || stop.Result != nil || !stop.Quiescent {
		return false
	}
	h := stop.Handle
	return boot != "" && boot == g.TargetBootID && h.BootID == boot && h.ID == g.ActionID && h.Digest == g.Digest && h.Epoch == g.Epoch && h.TopologyUID == g.Scope.TopologyUID && h.ResourceUID == g.Scope.ResourceUID && h.Generation == g.Scope.Generation && h.NodeID != "" && h.Revision > 0 && substrate.ValidateSSHAccessUser(h.User) == nil && sshAccessID.MatchString(h.AccessID)
}
func SignGuestSSHUnknownStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, host string, req substrate.SSHAccessMutation, stop substrate.SSHAccessStop, id string, now time.Time) (SignedExecutionStopReceipt, error) {
	if stop.Validate(g.Epoch, g.TargetBootID, req) != nil || !validSSHUnknownStop(g, g.TargetBootID, &stop) {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	digest, err := req.Digest()
	op := "guest_ssh_access_apply"
	if req.Delete {
		op = "guest_ssh_access_delete"
	}
	if err != nil || digest != g.Digest || g.Operation != op {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: host, GuestBootID: g.TargetBootID, SSHStop: &stop, ReceiptID: id, StoppedAt: now.UTC(), Quiescence: "confirmed"})
}
func VerifyGuestSSHUnknownStopReceipt(key ed25519.PublicKey, r SignedExecutionStopReceipt, req substrate.SSHAccessMutation, node string) error {
	if node == "" || VerifyExecutionStopReceipt(key, r) != nil || !validSSHUnknownStop(r.Grant, r.GuestBootID, r.SSHStop) || r.SSHStop.Handle.NodeID != node || r.SSHStop.Validate(r.Grant.Epoch, r.GuestBootID, req) != nil {
		return ErrInvalidExecutionGrant
	}
	digest, err := req.Digest()
	op := "guest_ssh_access_apply"
	if req.Delete {
		op = "guest_ssh_access_delete"
	}
	if err != nil || digest != r.Grant.Digest || op != r.Grant.Operation {
		return ErrInvalidExecutionGrant
	}
	return nil
}
