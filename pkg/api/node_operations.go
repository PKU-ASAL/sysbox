package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
)

type nodeOperationStore struct {
	store controlstore.Store
}

func newNodeOperationStore(store controlstore.Store) *nodeOperationStore {
	return &nodeOperationStore{store: store}
}

func prepareNodeOperation(op controlplane.NodeOperation) controlplane.NodeOperation {
	now := time.Now().UTC()
	if op.ID == "" {
		op.ID = uuid.New().String()
	}
	if op.Generation > 0 {
		op.ActionID = op.ID
	}
	if op.ProjectID == "" {
		op.ProjectID = "default"
	}
	if op.Workspace == "" {
		op.Workspace = op.Topology
	}
	if op.Status == "" {
		op.Status = controlplane.NodeOperationStatusQueued
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = now
	}
	actor := op.RequestedBy
	if actor == "" {
		actor = "api"
	}
	op.RequestedBy = actor
	if len(op.Audit) == 0 {
		resource := op.Resource()
		op.Audit = append(op.Audit, controlplane.Event{
			ProjectID: op.ProjectID,
			Workspace: op.Workspace,
			Resource:  resource,
			Action:    op.Operation,
			Status:    op.Status,
			Actor:     actor,
			Roles:     append([]string{}, op.Roles...),
			Message:   "node operation queued",
			CreatedAt: now,
		})
	}
	return op
}

func (s *nodeOperationStore) Create(op controlplane.NodeOperation) (controlplane.NodeOperation, error) {
	op = prepareNodeOperation(op)
	if s.store == nil {
		return op, fmt.Errorf("node operation store not configured")
	}
	if err := s.store.CreateNodeOperation(context.Background(), op); err != nil {
		return op, err
	}
	return op, nil
}

func (s *nodeOperationStore) CompareAndSwap(ctx context.Context, old, next controlplane.NodeOperation) error {
	if s.store == nil {
		return fmt.Errorf("node operation store not configured")
	}
	return s.store.CompareAndSwapNodeOperation(ctx, old, next)
}

func (s *nodeOperationStore) Get(id string) (*controlplane.NodeOperation, error) {
	if s.store == nil {
		return nil, fmt.Errorf("node operation store not configured")
	}
	return s.store.GetNodeOperation(context.Background(), id)
}
