package persistence

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// CommitFilesystemFenceDelete is a recoverable multi-file commit. The prepared
// decision is durable first and the grant stays revoked until all postimages
// have been fsynced. No lock file is removed; no provider operation is invoked.
func (r *Repository) CommitFilesystemFenceDelete(ctx context.Context, runsDir, topology string, request HostFenceReleaseRequest, key ed25519.PublicKey, load func() (controlplane.Run, CommandRecord, CommandRecord, error), persist func(controlplane.Run, CommandRecord) error) (FenceDeleteReconciliation, error) {
	var result FenceDeleteReconciliation
	backend, ok := r.backend.(*fileBackend)
	if !ok {
		return result, ErrUnsupportedVersion
	}
	records := &fileGuestRecords{root: runsDir}
	err := records.updateRecord(ctx, executionAuthorityKind, "authority", func(authority []byte) ([]byte, error) {
		ledger, err := decodeExecutionAuthority(authority)
		if err != nil {
			return nil, err
		}
		fence, ok := ledger.Fences[request.FenceID]
		if !ok {
			return nil, ErrNotFound
		}
		if rec := fence.Reconciliation; rec != nil {
			if rec.Key != request.Key || rec.ObservationCommandID != request.ObservationCommandID {
				return nil, ErrConflict
			}
			if rec.Completed {
				result = *rec
				return nil, nil
			}
		}
		run, original, observation, err := load()
		if err != nil {
			return nil, err
		}
		err = r.update(ctx, topology, func(a *aggregate) error {
			raw, err := json.Marshal(a)
			if err != nil {
				return err
			}
			transition, err := PrepareFenceDelete(authority, raw, run, original, observation, key, request, time.Now().UTC())
			if err != nil {
				return err
			}
			dir, err := records.directory(executionAuthorityKind)
			if err != nil {
				return err
			}
			authorityPath := filepath.Join(dir, "authority.json")
			if err = writePrivateGuestRecord(ctx, authorityPath, transition.PreparedAuthority); err != nil {
				return err
			}
			// Durable postimage before metadata retirement and grant stop publication.
			if err = writePrivateGuestRecord(ctx, filepath.Join(backend.root, topology+".json"), transition.Lifecycle); err != nil {
				return err
			}
			next, err := decodeAggregate(transition.Lifecycle)
			if err != nil {
				return err
			}
			*a = *next
			if err = persist(transition.Run, transition.Command); err != nil {
				return err
			}
			final, err := FinishFenceDelete(transition.PreparedAuthority, transition.Lifecycle, transition.Run, transition.Command, request)
			if err != nil {
				return err
			}
			if err = writePrivateGuestRecord(ctx, authorityPath, final); err != nil {
				return err
			}
			stored, err := decodeExecutionAuthority(final)
			if err != nil {
				return err
			}
			result = *stored.Fences[request.FenceID].Reconciliation
			return nil
		})
		return nil, err
	})
	return result, err
}
