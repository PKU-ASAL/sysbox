package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type nodeOperationStore struct {
	store apiStore
}

func newNodeOperationStore(store apiStore) *nodeOperationStore {
	return &nodeOperationStore{store: store}
}

func (s *nodeOperationStore) Create(op controlplane.NodeOperation) controlplane.NodeOperation {
	now := time.Now().UTC()
	if op.ID == "" {
		op.ID = uuid.New().String()
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
	s.Save(op)
	return op
}

func (s *nodeOperationStore) Save(op controlplane.NodeOperation) {
	if s.store != nil {
		_ = s.store.SaveNodeOperation(context.Background(), op)
	}
}

func (s *nodeOperationStore) Get(id string) (*controlplane.NodeOperation, error) {
	if s.store == nil {
		return nil, fmt.Errorf("node operation store not configured")
	}
	return s.store.GetNodeOperation(context.Background(), id)
}
