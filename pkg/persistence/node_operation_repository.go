package persistence

import (
	"context"
	"database/sql"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// NodeOperationRepository reuses the physical atomic record adapters. Save is
// create-only; every subsequent mutation must supply the exact prior snapshot.
type NodeOperationRepository struct{ records guestRecords }

func NewFilesystemNodeOperationRepository(root string) *NodeOperationRepository {
	return &NodeOperationRepository{records: &fileGuestRecords{root: root}}
}
func NewSQLiteNodeOperationRepository(db *sql.DB) *NodeOperationRepository {
	return &NodeOperationRepository{records: &sqliteGuestRecords{db: db}}
}
func NewPostgresNodeOperationRepository(pool *pgxpool.Pool) *NodeOperationRepository {
	return &NodeOperationRepository{records: &postgresGuestRecords{pool: pool}}
}
func (r *NodeOperationRepository) Create(ctx context.Context, op controlplane.NodeOperation) error {
	if !topologyName.MatchString(op.ID) {
		return ErrInvalidRecord
	}
	raw, err := EncodeNodeOperation(op)
	if err != nil {
		return err
	}
	return r.records.update(ctx, nodeOperationKind, op.ID, func(current []byte) ([]byte, error) {
		if current != nil {
			old, err := DecodeNodeOperation(current)
			if err != nil {
				return nil, err
			}
			if SameNodeOperation(*old, op) {
				return nil, nil
			}
			return nil, ErrConflict
		}
		if op.Status != controlplane.NodeOperationStatusQueued {
			return nil, ErrInvalidRecord
		}
		return raw, nil
	})
}
func (r *NodeOperationRepository) Get(ctx context.Context, id string) (*controlplane.NodeOperation, error) {
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	raw, err := r.records.get(ctx, nodeOperationKind, id)
	if err != nil {
		return nil, err
	}
	op, err := DecodeNodeOperation(raw)
	if err != nil {
		return nil, err
	}
	if op.ID != id {
		return nil, ErrCorruptRecord
	}
	return op, nil
}
func (r *NodeOperationRepository) CompareAndSwap(ctx context.Context, old, next controlplane.NodeOperation) error {
	if !topologyName.MatchString(old.ID) {
		return ErrInvalidRecord
	}
	if err := ValidateNodeOperationTransition(old, next); err != nil {
		return err
	}
	raw, err := EncodeNodeOperation(next)
	if err != nil {
		return err
	}
	return r.records.update(ctx, nodeOperationKind, old.ID, func(current []byte) ([]byte, error) {
		if current == nil {
			return nil, ErrNotFound
		}
		stored, err := DecodeNodeOperation(current)
		if err != nil {
			return nil, err
		}
		if !SameNodeOperation(*stored, old) {
			return nil, ErrConflict
		}
		return preserveGuestDispatch(current, raw)
	})
}
func (r *NodeOperationRepository) List(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error) {
	records, err := r.records.list(ctx, nodeOperationKind)
	if err != nil {
		return nil, err
	}
	var out []controlplane.NodeOperation
	for _, record := range records {
		op, err := DecodeNodeOperation(record.data)
		if err != nil {
			return nil, err
		}
		if op.ID != record.id {
			return nil, ErrCorruptRecord
		}
		w := op.Workspace
		if w == "" {
			w = op.Topology
		}
		if workspace == "" || workspace == w {
			out = append(out, *op)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
