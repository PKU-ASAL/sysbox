package controlplane

import (
	"crypto/ed25519"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

func IsGuestSSHAccessOperation(operation string) bool {
	return operation == "guest_ssh_access_apply" || operation == "guest_ssh_access_delete"
}

func validGuestSSHResult(g ExecutionGrant, guestBoot string, result *substrate.SSHAccessResult) bool {
	if result == nil {
		return false
	}
	h := result.Handle
	return guestBoot != "" && h.BootID == guestBoot && g.TargetBootID == guestBoot &&
		h.ID == g.ActionID && h.Digest == g.Digest && h.Epoch == g.Epoch &&
		h.TopologyUID == g.Scope.TopologyUID && h.ResourceUID != "" && h.ResourceUID == g.Scope.ResourceUID && h.Generation == g.Scope.Generation &&
		h.Revision > 0 && h.NodeID != "" && substrate.ValidateSSHAccessUser(h.User) == nil && sshAccessID.MatchString(h.AccessID) &&
		((g.Operation == "guest_ssh_access_apply" && result.Present) || (g.Operation == "guest_ssh_access_delete" && !result.Present))
}

func guestSSHResultMatchesRequest(g ExecutionGrant, req substrate.SSHAccessMutation, result substrate.SSHAccessResult) bool {
	digest, err := req.Digest()
	return err == nil && req.ExpectedRevision != math.MaxUint64 && req.ID == g.ActionID && digest == g.Digest &&
		result.Handle.User == req.Config.User && result.Handle.AccessID == req.Config.AccessID &&
		result.Handle.Revision == req.ExpectedRevision+1 && result.Present != req.Delete &&
		validGuestSSHResult(g, g.TargetBootID, &result)
}

// SignGuestSSHAccessStopReceipt may only be called after the host has verified
// this exact committed guest result. Absence alone or a cancelled RPC is not
// evidence that a mutation finished or is quiescent.
func SignGuestSSHAccessStopReceipt(key ed25519.PrivateKey, g ExecutionGrant, hostBoot string, req substrate.SSHAccessMutation, result substrate.SSHAccessResult, receiptID string, now time.Time) (SignedExecutionStopReceipt, error) {
	if !guestSSHResultMatchesRequest(g, req, result) {
		return SignedExecutionStopReceipt{}, ErrInvalidExecutionGrant
	}
	return signStopReceipt(key, SignedExecutionStopReceipt{Grant: g, HostBootID: hostBoot, GuestBootID: result.Handle.BootID,
		SSHResult: &result, ReceiptID: receiptID, StoppedAt: now.UTC(), Quiescence: "confirmed"})
}

// VerifyGuestSSHAccessStopReceipt binds signed evidence to the canonical
// mutation, including the user's requested CAS revision. The caller must also
// validate the grant against its durable authority before accepting completion.
func VerifyGuestSSHAccessStopReceipt(key ed25519.PublicKey, receipt SignedExecutionStopReceipt, req substrate.SSHAccessMutation, canonicalNode string) error {
	if canonicalNode == "" || VerifyExecutionStopReceipt(key, receipt) != nil || receipt.SSHResult == nil || receipt.SSHResult.Handle.NodeID != canonicalNode || !guestSSHResultMatchesRequest(receipt.Grant, req, *receipt.SSHResult) {
		return ErrInvalidExecutionGrant
	}
	return nil
}
