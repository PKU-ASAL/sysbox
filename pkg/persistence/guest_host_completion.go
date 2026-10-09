package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
)

// Caller authenticates the host signature and reserves its issued authority
// before entering this operation/command transaction. No worker lease is used.
func CompleteExecutionFromHost(raw []byte, record *CommandRecord, agent, id string, r controlplane.SignedExecutionStopReceipt, now time.Time) ([]byte, error) {
	x, err := decodeGuestRecord(id, raw, executionCodec)
	if err != nil {
		return nil, err
	}
	g := r.Grant
	if r.GuestResult == nil || r.GuestResult.Validate() != nil || g.Operation != "guest_task" || g.ParentEpoch != 0 || g.AgentID != agent || x.AgentID != agent || x.CommandID == "" || x.CommandID != g.CommandID || g.ActionID != id || r.TaskID != id || r.TaskDigest != g.Digest || r.GuestBootID != g.TargetBootID || r.Quiescence != "confirmed" || r.StoppedAt.IsZero() || x.CreatedAt.IsZero() {
		return nil, ErrConflict
	}
	body, err := json.Marshal(r)
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
		if json.Unmarshal(prior, &value) != nil || len(value) != 64 {
			return nil, ErrCorruptRecord
		}
		if value != hash {
			return nil, ErrConflict
		}
		return raw, nil
	}
	if guestTerminal(x.Status) || x.Version == math.MaxInt64 || record == nil {
		return nil, ErrConflict
	}
	cmd := record.Command
	if cmd.ID != x.CommandID || cmd.AgentID != agent || cmd.Type != "guest_execution" || cmd.TopologyUID != g.Scope.TopologyUID || cmd.ResourceUID != g.Scope.ResourceUID || cmd.Generation != g.Scope.Generation {
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
	if cmd.ID != g.CommandID || cmd.AgentID != agent || cmd.TopologyUID != g.Scope.TopologyUID || cmd.ResourceUID != g.Scope.ResourceUID || cmd.Generation != g.Scope.Generation || cmd.Execution == nil || cmd.Execution.ID != id || cmd.Execution.Topology != x.Topology || cmd.Execution.Node != x.Node || !cmd.Execution.CreatedAt.Equal(x.CreatedAt) {
		return nil, ErrConflict
	}
	task, err := guesttask.FromExecutionCommand(cmd)
	if err != nil {
		return nil, ErrConflict
	}
	digest, err := task.Digest()
	if err != nil || digest != g.Digest {
		return nil, ErrConflict
	}
	x.Result, x.ResultClass = r.GuestResult.Result, r.GuestResult.ResultClass
	x.Status = controlplane.GuestExecutionCompleted
	switch x.ResultClass {
	case "unknown":
		x.Status = controlplane.GuestExecutionFailed
		x.Err = "remote termination confirmed without exit status"
	case "cancelled":
		x.Status = controlplane.GuestExecutionCancelled
		x.Err = ""
	default:
		x.Err = ""
	}
	// The host clock can be skewed relative to admission. Preserve its signed
	// timestamp in the receipt and use the API observation time in this model.
	x.EndedAt = now
	x.ExpiresAt = now.Add(15 * time.Minute)
	x.Version++
	next, err := executionCodec.encode(x)
	if err != nil {
		return nil, err
	}
	next, err = preserveGuestDispatch(raw, next)
	if err != nil {
		return nil, err
	}
	object = nil
	if err = decodeGuestObject(next, &object); err != nil {
		return nil, err
	}
	object["host_completion_receipt"], err = json.Marshal(hash)
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}
