package persistence

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type GuestCompletion struct {
	HostFile      *controlplane.SignedExecutionStopReceipt
	HostExecution *controlplane.SignedExecutionStopReceipt
}

func (c GuestCompletion) kind() (guestKind, error) {
	if c.HostFile != nil && c.HostExecution == nil {
		return guestFileKind, nil
	}
	if c.HostExecution != nil && c.HostFile == nil {
		return guestExecutionKind, nil
	}
	return "", ErrInvalidRecord
}
func (c GuestCompletion) Table() (string, error) { k, err := c.kind(); return string(k), err }
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

// CompleteGuestRecord applies the result bound to a verified host receipt.
func CompleteGuestRecord(raw []byte, cmd *CommandRecord, agent, id string, c GuestCompletion, now time.Time) ([]byte, error) {
	if _, err := c.kind(); err != nil {
		return nil, err
	}
	if c.HostFile != nil {
		return CompleteFileFromHost(raw, cmd, agent, id, *c.HostFile, now)
	}
	return CompleteExecutionFromHost(raw, cmd, agent, id, *c.HostExecution, now)
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
