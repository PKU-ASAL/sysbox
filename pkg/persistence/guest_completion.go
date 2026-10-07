package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"math"
	"time"
)

type GuestCompletion struct {
	HostFile  *controlplane.SignedExecutionStopReceipt
	Execution *controlplane.GuestExecutionCompletion
	File      *controlplane.GuestFileOperationCompletion
}

func (c GuestCompletion) kind() (guestKind, error) {
	if c.HostFile != nil && c.File == nil && c.Execution == nil {
		return guestFileKind, nil
	}
	if c.Execution != nil && c.File == nil {
		return guestExecutionKind, nil
	}
	if c.File != nil && c.Execution == nil {
		return guestFileKind, nil
	}
	return "", ErrInvalidRecord
}
func (c GuestCompletion) Table() (string, error) { k, err := c.kind(); return string(k), err }
func (c GuestCompletion) proof() controlplane.AgentCommandEvent {
	if x := c.Execution; x != nil {
		return controlplane.AgentCommandEvent{ActionID: x.ActionID, ResourceUID: x.ResourceUID, Generation: x.Generation, LeaseEpoch: x.LeaseEpoch, LeaseOwner: x.LeaseOwner, Attempt: x.Attempt}
	}
	if x := c.File; x != nil {
		return controlplane.AgentCommandEvent{ActionID: x.ActionID, ResourceUID: x.ResourceUID, Generation: x.Generation, LeaseEpoch: x.LeaseEpoch, LeaseOwner: x.LeaseOwner, Attempt: x.Attempt}
	}
	return controlplane.AgentCommandEvent{}
}
func (c GuestCompletion) Binding(raw []byte, id string) (string, string, error) {
	k, err := c.kind()
	if err != nil {
		return "", "", err
	}
	if k == guestExecutionKind {
		x, err := decodeGuestRecord(id, raw, executionCodec)
		return x.AgentID, x.CommandID, err
	}
	x, err := decodeGuestRecord(id, raw, fileOperationCodec)
	return x.AgentID, x.CommandID, err
}

// CompleteGuestRecord executes under the adapter's operation+command lock.
// The receipt hashes both result and lease proof without retaining extra output.
func CompleteGuestRecord(raw []byte, cmd *CommandRecord, agent, id string, c GuestCompletion, now time.Time) ([]byte, error) {
	if c.HostFile != nil {
		return CompleteFileFromHost(raw, cmd, agent, id, *c.HostFile, now)
	}
	// A worker cannot prove guest installation commit or cancellation. File
	// terminal results are accepted only through the authenticated host path.
	if c.File != nil {
		return nil, ErrConflict
	}
	owner, commandID, err := c.Binding(raw, id)
	if err != nil {
		return nil, err
	}
	proof := c.proof()
	if owner != agent || commandID == "" || proof.ActionID == "" || proof.ResourceUID == "" || proof.Generation < 1 || proof.LeaseEpoch < 1 || proof.LeaseOwner == "" || proof.Attempt < 1 {
		return nil, ErrConflict
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	receipt := hex.EncodeToString(sum[:])
	var object map[string]json.RawMessage
	if err := decodeGuestObject(raw, &object); err != nil {
		return nil, err
	}
	var prior string
	if recorded, ok := object["completion_receipt"]; ok {
		if json.Unmarshal(recorded, &prior) != nil || len(prior) != 64 {
			return nil, ErrCorruptRecord
		}
		if prior == receipt {
			return raw, nil
		}
		return nil, ErrConflict
	}
	if cmd == nil || cmd.Retired {
		return nil, ErrConflict
	}
	command := cmd.Command
	if command.ID != commandID || command.AgentID != agent || command.IsTerminal() || command.Status == controlplane.AgentCommandStatusQueued || !command.LeaseUntil.After(now) || command.ActionID != proof.ActionID || command.ResourceUID != proof.ResourceUID || command.Generation != proof.Generation || command.LeaseEpoch != proof.LeaseEpoch || command.LeaseOwner != proof.LeaseOwner || command.Attempt != proof.Attempt {
		return nil, ErrConflict
	}
	var next []byte
	if c.Execution != nil {
		x, err := executionCodec.decode(raw)
		if err != nil {
			return nil, err
		}
		if command.Type != "guest_execution" || command.Execution == nil || command.Execution.ID != id || command.Execution.Topology != x.Topology || command.Execution.Node != x.Node || guestTerminal(x.Status) || x.Version == math.MaxInt64 {
			return nil, ErrConflict
		}
		x.Status = controlplane.GuestExecutionCompleted
		if c.Execution.Error != "" {
			x.Status = controlplane.GuestExecutionFailed
		}
		x.Result, x.ResultClass, x.Err = c.Execution.Result, c.Execution.ResultClass, c.Execution.Error
		x.EndedAt = now
		x.ExpiresAt = now.Add(15 * time.Minute)
		x.Version++
		next, err = executionCodec.encode(x)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, ErrInvalidRecord
	}
	next, err = preserveGuestDispatch(raw, next)
	if err != nil {
		return nil, err
	}
	object = nil
	if err := decodeGuestObject(next, &object); err != nil {
		return nil, err
	}
	object["completion_receipt"], err = json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}

func (r *GuestRepository) CompleteUnderCommandLock(ctx context.Context, agent, id string, c GuestCompletion, cmd *CommandRecord) ([]byte, error) {
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	k, err := c.kind()
	if err != nil {
		return nil, err
	}
	var completed []byte
	err = r.records.update(ctx, k, id, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, ErrNotFound
		}
		var err error
		completed, err = CompleteGuestRecord(raw, cmd, agent, id, c, time.Now().UTC())
		return completed, err
	})
	if err != nil {
		return nil, err
	}
	return completed, nil
}

func DecodeCompletedGuestExecution(raw []byte) (controlplane.GuestExecution, error) {
	return executionCodec.decode(raw)
}
func DecodeCompletedGuestFile(raw []byte) (controlplane.GuestFileOperation, error) {
	return fileOperationCodec.decode(raw)
}
