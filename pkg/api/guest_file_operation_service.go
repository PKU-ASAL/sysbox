package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

var (
	errGuestFileOperationNotFound = errors.New("guest file operation not found")
	errGuestFileOperationConflict = errors.New("guest file operation conflict")
)

type GuestFileOperationService struct {
	store guestFileOperationPersistence
	now   func() time.Time
}

func newGuestFileOperationService(store guestFileOperationPersistence) *GuestFileOperationService {
	return &GuestFileOperationService{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (s *GuestFileOperationService) Get(ctx context.Context, id string) (controlplane.GuestFileOperation, error) {
	op, err := s.store.GetGuestFileOperation(ctx, id)
	if err != nil {
		if errors.Is(err, errGuestFileOperationStoreNotFound) {
			return controlplane.GuestFileOperation{}, errGuestFileOperationNotFound
		}
		return controlplane.GuestFileOperation{}, err
	}
	return *op, nil
}

func (s *GuestFileOperationService) MarkRunning(ctx context.Context, id string) (controlplane.GuestFileOperation, error) {
	op, err := s.Get(ctx, id)
	if err != nil {
		return op, err
	}
	if op.CancelRequested {
		return op, errGuestFileOperationConflict
	}
	if op.Status != controlplane.GuestExecutionQueued {
		if op.Status == controlplane.GuestExecutionRunning {
			return op, nil
		}
		return op, errGuestFileOperationConflict
	}
	op.Status, op.StartedAt = controlplane.GuestExecutionRunning, s.now()
	ok, err := s.store.CompareAndSwapGuestFileOperation(ctx, op, op.Version)
	if err == nil && !ok {
		err = fmt.Errorf("guest file operation conflict")
	}
	if ok {
		op.Version++
	}
	return op, err
}

func (s *GuestFileOperationService) Complete(ctx context.Context, id, errText string) (controlplane.GuestFileOperation, error) {
	op, err := s.Get(ctx, id)
	if err != nil {
		return op, err
	}
	if op.CancelRequested {
		return op, errGuestFileOperationConflict
	}
	if op.Status == controlplane.GuestExecutionCompleted || op.Status == controlplane.GuestExecutionFailed {
		return op, nil
	}
	if op.Status != controlplane.GuestExecutionRunning {
		return op, errGuestFileOperationConflict
	}
	op.Status = controlplane.GuestExecutionCompleted
	if errText != "" {
		op.Status, op.Error = controlplane.GuestExecutionFailed, "file operation failed"
	}
	op.EndedAt = s.now()
	ok, err := s.store.CompareAndSwapGuestFileOperation(ctx, op, op.Version)
	if err == nil && !ok {
		err = fmt.Errorf("guest file operation conflict")
	}
	if ok {
		op.Version++
	}
	return op, err
}

func (s *GuestFileOperationService) RequestCancel(ctx context.Context, id string) (controlplane.GuestFileOperation, error) {
	op, err := s.Get(ctx, id)
	if err != nil {
		return op, err
	}
	if guestExecutionTerminal(op.Status) || op.CancelRequested {
		return op, nil
	}
	op.CancelRequested = true
	ok, err := s.store.CompareAndSwapGuestFileOperation(ctx, op, op.Version)
	if err == nil && !ok {
		err = errGuestFileOperationConflict
	}
	if ok {
		op.Version++
	}
	return op, err
}

func (s *GuestFileOperationService) ReconcileCommandFailure(ctx context.Context, id, status string) error {
	op, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if guestExecutionTerminal(op.Status) {
		return nil
	}
	if status == controlplane.AgentCommandStatusCancelled || op.CancelRequested {
		op.Status = controlplane.GuestExecutionCancelled
	} else {
		op.Status, op.Error = controlplane.GuestExecutionFailed, "file operation failed"
	}
	op.EndedAt = s.now()
	ok, err := s.store.CompareAndSwapGuestFileOperation(ctx, op, op.Version)
	if err == nil && !ok {
		err = errGuestFileOperationConflict
	}
	return err
}

func (s *GuestFileOperationService) Cancel(ctx context.Context, id string) (controlplane.GuestFileOperation, error) {
	op, err := s.Get(ctx, id)
	if err != nil {
		return op, err
	}
	if guestExecutionTerminal(op.Status) {
		return op, nil
	}
	op.Status, op.EndedAt = controlplane.GuestExecutionCancelled, s.now()
	ok, err := s.store.CompareAndSwapGuestFileOperation(ctx, op, op.Version)
	if err == nil && !ok {
		err = fmt.Errorf("guest file operation conflict")
	}
	if ok {
		op.Version++
	}
	return op, err
}
