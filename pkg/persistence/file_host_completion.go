package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

// CompleteFileFromHost requires authenticated, validated host evidence. The
// historical dispatch binding is checked even when its worker/session expired.
func CompleteFileFromHost(raw []byte, record *CommandRecord, agent, id string, receipt controlplane.SignedExecutionStopReceipt, now time.Time) ([]byte, error) {
	x, err := fileOperationCodec.decode(raw)
	if err != nil {
		return nil, err
	}
	if record == nil || receipt.FileResult == nil || !receipt.FileResult.Quiescent || receipt.Grant.Operation != "guest_file_install" {
		return nil, ErrConflict
	}
	cmd := record.Command
	g := receipt.Grant
	r := receipt.FileResult
	if cmd.ID != x.CommandID || cmd.AgentID != agent || cmd.Type != "guest_file_put" || cmd.TopologyUID != g.Scope.TopologyUID || cmd.ResourceUID != g.Scope.ResourceUID || cmd.Generation != g.Scope.Generation {
		return nil, ErrConflict
	}
	if record.Retired {
		seed, err := guestDispatchSeed(raw)
		if err != nil {
			return nil, err
		}
		if seed == nil {
			return nil, ErrConflict
		}
		cmd = seed.Command
	}
	req, err := controlplane.FromFileInstallCommand(cmd)
	if err != nil {
		return nil, ErrConflict
	}
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	if x.ID != id || x.AgentID != agent || x.CommandID != cmd.ID || cmd.AgentID != agent || g.AgentID != agent || g.CommandID != cmd.ID || g.ActionID != id || receipt.InstallID != id || receipt.InstallDigest != digest || g.Digest != digest || g.Scope.TopologyUID != cmd.TopologyUID || g.Scope.ResourceUID != cmd.ResourceUID || g.Scope.Generation != cmd.Generation || cmd.FilePut.Topology != x.Topology || cmd.FilePut.Node != x.Node {
		return nil, ErrConflict
	}
	rebootUnknown := req.RebootScope == "guest-tmpfs" && r.Outcome == substrate.FileInstallUnknown && receipt.ObservedGuestBootID != "" && receipt.GuestBootID == g.TargetBootID && receipt.ObservedGuestBootID != receipt.GuestBootID && r.SHA256 == "" && r.Size == 0 && r.Mode == 0
	scopedUnknown := r.Outcome == substrate.FileInstallUnknown && r.StoppedEpoch == g.Epoch && g.Epoch > 0 && receipt.ObservedGuestBootID == "" && receipt.GuestBootID == g.TargetBootID && r.SHA256 == req.SHA256 && r.Size == req.Size && r.Mode == req.Mode
	if r.Outcome != substrate.FileInstallCommitted && r.Outcome != substrate.FileInstallCancelled && !rebootUnknown && !scopedUnknown {
		return nil, ErrConflict
	}
	if r.Outcome == substrate.FileInstallCommitted && (r.SHA256 != req.SHA256 || r.Size != req.Size || r.Mode != req.Mode) {
		return nil, ErrConflict
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	var object map[string]json.RawMessage
	if err = decodeGuestObject(raw, &object); err != nil {
		return nil, err
	}
	if prior, ok := object["host_completion_receipt"]; ok {
		var value string
		if json.Unmarshal(prior, &value) != nil || value != hash {
			return nil, ErrConflict
		}
		return raw, nil
	}
	if guestTerminal(x.Status) || x.Version == math.MaxInt64 {
		return nil, ErrConflict
	}
	x.Status = controlplane.GuestExecutionCompleted
	if rebootUnknown || scopedUnknown {
		x.Status = controlplane.GuestExecutionFailed
		x.Error = "guest reboot confirmed; original file result unknown"
		if scopedUnknown {
			x.Error = "guest writer scope stopped; original file result unknown"
		}
	}
	if r.Outcome == substrate.FileInstallCancelled {
		x.Status = controlplane.GuestExecutionCancelled
	}
	result := *r
	x.Result = &result
	x.EndedAt = now
	x.Version++
	next, err := fileOperationCodec.encode(x)
	if err != nil {
		return nil, err
	}
	next, err = preserveGuestDispatch(raw, next)
	if err != nil {
		return nil, err
	}
	if err = decodeGuestObject(next, &object); err != nil {
		return nil, err
	}
	object["host_completion_receipt"], err = json.Marshal(hash)
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}
