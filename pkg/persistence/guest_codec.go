package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const GuestRecordFormat = 2
const GuestFileRecordFormat = 2

var ErrInvalidRecord = errors.New("invalid repository record")

// Keep a typed diagnostic cause without putting stored values into a public
// error string (time.ParseError, for example, embeds the entire bad value).
type guestDecodeError struct{ cause error }

func (e *guestDecodeError) Error() string        { return "corrupt repository record: decode guest metadata" }
func (e *guestDecodeError) Unwrap() error        { return e.cause }
func (e *guestDecodeError) Is(target error) bool { return target == ErrCorruptRecord }

type storedExecution struct {
	controlplane.GuestExecution
	Format    int                                `json:"record_format"`
	AgentID   string                             `json:"agent_id"`
	CommandID string                             `json:"command_id,omitempty"`
	Request   controlplane.GuestExecutionRequest `json:"request"`
}
type storedFileOperation struct {
	controlplane.GuestFileOperation
	Format          int    `json:"record_format"`
	AgentID         string `json:"agent_id"`
	CommandID       string `json:"command_id,omitempty"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
}

type guestCodec[T any] struct {
	encode     func(T) ([]byte, error)
	decode     func([]byte) (T, error)
	id         func(T) string
	version    func(T) int64
	setVersion func(*T, int64)
	validate   func(T) error
	transition func(T, T) error
}

func validGuestStatus(status string) bool {
	switch status {
	case controlplane.GuestExecutionQueued, controlplane.GuestExecutionRunning, controlplane.GuestExecutionCompleted, controlplane.GuestExecutionFailed, controlplane.GuestExecutionCancelled:
		return true
	default:
		return false
	}
}
func guestTerminal(status string) bool {
	return status == controlplane.GuestExecutionCompleted || status == controlplane.GuestExecutionFailed || status == controlplane.GuestExecutionCancelled
}
func validateGuestIdentity(id string, version int64, status string) error {
	if !topologyName.MatchString(id) || version < 1 || !validGuestStatus(status) {
		return ErrInvalidRecord
	}
	return nil
}
func decodeGuestObject(raw []byte, out any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return ErrCorruptRecord
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &guestDecodeError{cause: err}
	}
	return nil
}
func validateGuestFormat(format, expected int, err error) error {
	if format != expected {
		return fmt.Errorf("%w: guest metadata requires explicit rebuild", ErrUnsupportedVersion)
	}
	if err != nil {
		return fmt.Errorf("%w: invalid guest identity, version or status", ErrCorruptRecord)
	}
	return nil
}

func encodeExecution(x controlplane.GuestExecution) ([]byte, error) {
	return json.Marshal(storedExecution{GuestExecution: x, Format: GuestRecordFormat, AgentID: x.AgentID, CommandID: x.CommandID, Request: x.Request})
}
func encodeFileOperation(x controlplane.GuestFileOperation) ([]byte, error) {
	return json.Marshal(storedFileOperation{GuestFileOperation: x, Format: GuestFileRecordFormat, AgentID: x.AgentID, CommandID: x.CommandID, CancelRequested: x.CancelRequested})
}

var executionCodec = guestCodec[controlplane.GuestExecution]{
	id:         func(x controlplane.GuestExecution) string { return x.ID },
	version:    func(x controlplane.GuestExecution) int64 { return x.Version },
	setVersion: func(x *controlplane.GuestExecution, v int64) { x.Version = v },
	validate:   func(x controlplane.GuestExecution) error { return validateGuestIdentity(x.ID, x.Version, x.Status) },
	encode:     encodeExecution,
	decode: func(raw []byte) (controlplane.GuestExecution, error) {
		var stored storedExecution
		if err := decodeGuestObject(raw, &stored); err != nil {
			return controlplane.GuestExecution{}, err
		}
		x := stored.GuestExecution
		x.AgentID, x.CommandID, x.Request = stored.AgentID, stored.CommandID, stored.Request
		return x, validateGuestFormat(stored.Format, GuestRecordFormat, validateGuestIdentity(x.ID, x.Version, x.Status))
	},
	transition: executionTransition,
}
var fileOperationCodec = guestCodec[controlplane.GuestFileOperation]{
	id:         func(x controlplane.GuestFileOperation) string { return x.ID },
	version:    func(x controlplane.GuestFileOperation) int64 { return x.Version },
	setVersion: func(x *controlplane.GuestFileOperation, v int64) { x.Version = v },
	validate:   validateFileOperation,
	encode:     encodeFileOperation,
	decode: func(raw []byte) (controlplane.GuestFileOperation, error) {
		var stored storedFileOperation
		if err := decodeGuestObject(raw, &stored); err != nil {
			return controlplane.GuestFileOperation{}, err
		}
		x := stored.GuestFileOperation
		x.AgentID, x.CommandID, x.CancelRequested = stored.AgentID, stored.CommandID, stored.CancelRequested
		return x, validateGuestFormat(stored.Format, GuestFileRecordFormat, validateFileOperation(x))
	},
	transition: fileOperationTransition,
}

func validateFileOperation(x controlplane.GuestFileOperation) error {
	if x.CancelRequested && x.Status == controlplane.GuestExecutionCompleted && x.Result == nil {
		return ErrInvalidRecord
	}
	return validateGuestIdentity(x.ID, x.Version, x.Status)
}
func sameGuestRequest(a, b controlplane.GuestExecutionRequest) bool {
	// Match the on-disk codec: omitempty intentionally treats nil and empty
	// environments alike, and may normalize them on a round trip.
	x, xerr := json.Marshal(a)
	y, yerr := json.Marshal(b)
	return xerr == nil && yerr == nil && bytes.Equal(x, y)
}

func statusTransition(from, to string, file bool) error {
	if from == to {
		return nil
	}
	if guestTerminal(from) || to == controlplane.GuestExecutionQueued {
		return ErrConflict
	}
	if file && from == controlplane.GuestExecutionQueued && to == controlplane.GuestExecutionCompleted {
		return ErrConflict
	}
	return nil
}
func commandBinding(old, next string) bool { return old == "" || old == next }
func executionTransition(old, next controlplane.GuestExecution) error {
	if old.ID != next.ID || old.Topology != next.Topology || old.Node != next.Node || old.AgentID != next.AgentID || !commandBinding(old.CommandID, next.CommandID) || !old.CreatedAt.Equal(next.CreatedAt) {
		return ErrConflict
	}
	if old.CancelRequested && (!next.CancelRequested || next.CommandID != old.CommandID || next.Status != old.Status && !guestTerminal(next.Status)) {
		return ErrConflict
	}
	if !old.CancelRequested && next.CancelRequested && next.Status != old.Status {
		return ErrConflict
	}
	if err := statusTransition(old.Status, next.Status, false); err != nil {
		return err
	}
	if !guestTerminal(old.Status) {
		if !sameGuestRequest(old.Request, next.Request) {
			return ErrConflict
		}
		return nil
	}
	// Terminal records are immutable except one-way payload expiry redaction.
	if !sameGuestRequest(old.Request, next.Request) && !sameGuestRequest(next.Request, controlplane.GuestExecutionRequest{}) {
		return ErrConflict
	}
	if next.Result.Stdout != old.Result.Stdout && next.Result.Stdout != "" || next.Result.Stderr != old.Result.Stderr && next.Result.Stderr != "" {
		return ErrConflict
	}
	old.Request, next.Request = controlplane.GuestExecutionRequest{}, controlplane.GuestExecutionRequest{}
	old.Result.Stdout, next.Result.Stdout, old.Result.Stderr, next.Result.Stderr = "", "", "", ""
	old.Version, next.Version = 0, 0
	old.CommandID = next.CommandID
	if !sameGuestRecord(old, next, encodeExecution) {
		return ErrConflict
	}
	return nil
}
func fileOperationTransition(old, next controlplane.GuestFileOperation) error {
	if old.ID != next.ID || old.Topology != next.Topology || old.Node != next.Node || old.AgentID != next.AgentID || !commandBinding(old.CommandID, next.CommandID) || !old.CreatedAt.Equal(next.CreatedAt) {
		return ErrConflict
	}
	if old.CancelRequested && (!next.CancelRequested || next.Status != old.Status && next.Status != controlplane.GuestExecutionCancelled) {
		return ErrConflict
	}
	if err := statusTransition(old.Status, next.Status, true); err != nil {
		return err
	}
	if guestTerminal(old.Status) {
		old.Version, next.Version = 0, 0
		old.CommandID = next.CommandID
		if !sameGuestRecord(old, next, encodeFileOperation) {
			return ErrConflict
		}
	}
	return nil
}
func sameGuestRecord[T any](a, b T, encode func(T) ([]byte, error)) bool {
	x, xerr := encode(a)
	y, yerr := encode(b)
	return xerr == nil && yerr == nil && bytes.Equal(x, y)
}
