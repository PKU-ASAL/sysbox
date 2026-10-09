package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"
)

// SnapshotRestoreRequest binds one immutable metadata restoration. The caller
// must retain the matching maintenance barrier through commit and receipt read.
type SnapshotRestoreRequest struct {
	ID               string `json:"id"`
	TopologyUID      string `json:"topology_uid"`
	SnapshotID       string `json:"snapshot_id"`
	SnapshotDigest   string `json:"snapshot_digest"`
	ExpectedSerial   int64  `json:"expected_serial"`
	MaintenanceEpoch uint64 `json:"maintenance_epoch"`
}

type SnapshotRestoreReceipt struct {
	Request     SnapshotRestoreRequest `json:"request"`
	Serial      int64                  `json:"serial"`
	CompletedAt time.Time              `json:"completed_at"`
}

// Digest binds the immutable preflight intent before the authority allocates
// an epoch. Commit requires that allocated epoch separately; this digest alone
// never authorizes a restore.
func (request SnapshotRestoreRequest) Digest() (string, error) {
	if !validSnapshotRestoreDraft(request) {
		return "", ErrInvalidRecord
	}
	request.MaintenanceEpoch = 0
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("sysbox.snapshot-restore.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func validSnapshotRestoreDraft(request SnapshotRestoreRequest) bool {
	request.MaintenanceEpoch = 1
	return validSnapshotRestoreRequest(request)
}

func validSnapshotRestoreRequest(request SnapshotRestoreRequest) bool {
	digest, err := hex.DecodeString(request.SnapshotDigest)
	return topologyName.MatchString(request.ID) && request.TopologyUID != "" && request.SnapshotID != "" && err == nil && len(digest) == 32 && request.ExpectedSerial >= 0 && request.ExpectedSerial < math.MaxInt64 && request.MaintenanceEpoch > 0
}

// PlanSnapshotRestore validates feasibility without changing state or acquiring
// execution rights. Epoch zero produces a preflight draft for BeginMaintenance.
func (r *Repository) PlanSnapshotRestore(ctx context.Context, topology, snapshotID, requestID string, epoch uint64) (SnapshotRestoreRequest, error) {
	var plan SnapshotRestoreRequest
	err := r.update(ctx, topology, func(a *aggregate) error {
		if prior, ok := a.Restores[requestID]; ok {
			if prior.Request.SnapshotID != snapshotID || epoch != 0 && prior.Request.MaintenanceEpoch != epoch {
				return ErrConflict
			}
			plan = prior.Request
			return nil
		}
		snapshot, ok := a.Snapshots[snapshotID]
		if !ok {
			return ErrNotFound
		}
		if _, err := prepareSnapshotRestore(a, snapshotID); err != nil {
			return err
		}
		plan = SnapshotRestoreRequest{ID: requestID, TopologyUID: a.State.TopologyUID, SnapshotID: snapshotID, SnapshotDigest: snapshot.Digest, ExpectedSerial: a.Serial, MaintenanceEpoch: epoch}
		if !validSnapshotRestoreDraft(plan) {
			return ErrInvalidRecord
		}
		return nil
	})
	return plan, err
}

// CommitSnapshotRestore atomically writes state and its immutable receipt. It
// does not itself validate maintenance authority; use only inside the caller's
// authorized maintenance workflow. Exact retries are historical reads, even if
// a successor has advanced state or snapshot retention removed the source.
func (r *Repository) CommitSnapshotRestore(ctx context.Context, topology string, request SnapshotRestoreRequest) (SnapshotRestoreReceipt, error) {
	var receipt SnapshotRestoreReceipt
	if !validSnapshotRestoreRequest(request) {
		return receipt, ErrConflict
	}
	err := r.update(ctx, topology, func(a *aggregate) error {
		if prior, ok := a.Restores[request.ID]; ok {
			if prior.Request != request {
				return ErrConflict
			}
			receipt = prior
			return nil
		}
		if request.TopologyUID != a.State.TopologyUID || request.ExpectedSerial != a.Serial {
			return ErrConflict
		}
		snapshot, ok := a.Snapshots[request.SnapshotID]
		if !ok {
			return ErrNotFound
		}
		if snapshot.Digest != request.SnapshotDigest {
			return ErrConflict
		}
		if err := restoreSnapshot(a, request.SnapshotID); err != nil {
			return err
		}
		receipt = SnapshotRestoreReceipt{Request: request, Serial: a.Serial, CompletedAt: a.UpdatedAt}
		a.Restores[request.ID] = receipt
		return nil
	})
	return receipt, err
}
