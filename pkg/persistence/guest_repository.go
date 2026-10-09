package persistence

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type guestKind string

const (
	guestExecutionKind guestKind = "sysbox_guest_executions"
	guestFileKind      guestKind = "sysbox_guest_file_operations"
	nodeOperationKind  guestKind = "sysbox_node_operations"
)

type guestRawRecord struct {
	id   string
	data []byte
}

// Physical adapters never decide status transitions, versions or identities.
type guestRecords interface {
	get(context.Context, guestKind, string) ([]byte, error)
	list(context.Context, guestKind) ([]guestRawRecord, error)
	update(context.Context, guestKind, string, func([]byte) ([]byte, error)) error
}

// GuestRepository owns both guest-operation aggregate contracts. Constructors
// borrow existing database pools; it does not own or close those pools.
type GuestRepository struct{ records guestRecords }

func (r *GuestRepository) SaveGuestExecution(ctx context.Context, x controlplane.GuestExecution) error {
	if x.CancelRequested {
		return ErrConflict
	}
	return createGuestRecord(ctx, r.records, guestExecutionKind, x, executionCodec)
}
func (r *GuestRepository) GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error) {
	return getGuestRecord(ctx, r.records, guestExecutionKind, id, executionCodec)
}

// ReadGuestExecutionRecord is used by the API transaction adapters when they
// must preserve opaque dispatch and completion evidence fields.
func (r *GuestRepository) ReadGuestExecutionRecord(ctx context.Context, id string) ([]byte, error) {
	return r.records.get(ctx, guestExecutionKind, id)
}

// DecodeGuestExecution validates a record read inside a caller-owned transaction.
func DecodeGuestExecution(id string, raw []byte) (controlplane.GuestExecution, error) {
	return decodeGuestRecord(id, raw, executionCodec)
}
func (r *GuestRepository) CompareAndSwapGuestExecution(ctx context.Context, x controlplane.GuestExecution, expected int64) (bool, error) {
	return swapGuestRecord(ctx, r.records, guestExecutionKind, x, expected, executionCodec)
}
func (r *GuestRepository) SaveGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation) error {
	return createGuestRecord(ctx, r.records, guestFileKind, x, fileOperationCodec)
}
func (r *GuestRepository) GetGuestFileOperation(ctx context.Context, id string) (*controlplane.GuestFileOperation, error) {
	return getGuestRecord(ctx, r.records, guestFileKind, id, fileOperationCodec)
}
func (r *GuestRepository) CompareAndSwapGuestFileOperation(ctx context.Context, x controlplane.GuestFileOperation, expected int64) (bool, error) {
	return swapGuestRecord(ctx, r.records, guestFileKind, x, expected, fileOperationCodec)
}

func decodeGuestRecord[T any](id string, raw []byte, codec guestCodec[T]) (T, error) {
	x, err := codec.decode(raw)
	if err != nil {
		return x, err
	}
	if codec.id(x) != id {
		return x, fmt.Errorf("%w: guest record key/identity mismatch", ErrCorruptRecord)
	}
	return x, nil
}
func getGuestRecord[T any](ctx context.Context, records guestRecords, kind guestKind, id string, codec guestCodec[T]) (*T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	raw, err := records.get(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	x, err := decodeGuestRecord(id, raw, codec)
	if err != nil {
		return nil, err
	}
	return &x, nil
}
func createGuestRecord[T any](ctx context.Context, records guestRecords, kind guestKind, x T, codec guestCodec[T]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codec.validate(x); err != nil {
		return err
	}
	return records.update(ctx, kind, codec.id(x), func(raw []byte) ([]byte, error) {
		if raw != nil {
			old, err := decodeGuestRecord(codec.id(x), raw, codec)
			if err != nil {
				return nil, err
			}
			if sameGuestRecord(old, x, codec.encode) {
				return nil, nil
			}
			return nil, ErrConflict
		}
		return codec.encode(x)
	})
}
func swapGuestRecord[T any](ctx context.Context, records guestRecords, kind guestKind, x T, expected int64, codec guestCodec[T]) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if expected < 1 || expected == math.MaxInt64 || codec.version(x) != expected {
		return false, ErrInvalidRecord
	}
	if err := codec.validate(x); err != nil {
		return false, err
	}
	swapped := false
	err := records.update(ctx, kind, codec.id(x), func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, ErrNotFound
		}
		old, err := decodeGuestRecord(codec.id(x), raw, codec)
		if err != nil {
			return nil, err
		}
		if codec.version(old) != expected {
			return nil, nil
		}
		if err := codec.transition(old, x); err != nil {
			return nil, err
		}
		codec.setVersion(&x, expected+1)
		next, err := codec.encode(x)
		if err == nil {
			next, err = preserveGuestDispatch(raw, next)
		}
		swapped = err == nil
		return next, err
	})
	return swapped && err == nil, err
}
func listGuestRecords[T any](ctx context.Context, records guestRecords, kind guestKind, codec guestCodec[T]) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := records.list(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		x, err := decodeGuestRecord(row.id, row.data, codec)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}
func (r *GuestRepository) LatestGuestAcceptance(ctx context.Context, agentID string) (time.Time, time.Time, error) {
	executions, err := listGuestRecords(ctx, r.records, guestExecutionKind, executionCodec)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	files, err := listGuestRecords(ctx, r.records, guestFileKind, fileOperationCodec)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var execAt, fileAt time.Time
	for _, x := range executions {
		if x.AgentID == agentID && x.Status == controlplane.GuestExecutionCompleted && x.ResultClass == controlplane.GuestExecutionResultClassExit && x.Err == "" && x.EndedAt.After(execAt) {
			execAt = x.EndedAt
		}
	}
	for _, x := range files {
		if x.AgentID == agentID && x.Status == controlplane.GuestExecutionCompleted && x.Error == "" && x.EndedAt.After(fileAt) {
			fileAt = x.EndedAt
		}
	}
	return execAt.UTC(), fileAt.UTC(), nil
}
