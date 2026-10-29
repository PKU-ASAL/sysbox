package persistence

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// CommitFilesystemFenceIntent requires the caller's operation/admission lock.
// load must reject every unrelated active operation while that lock is held.
// Authority is released only after all exact postimages are durably published.
func (r *Repository) CommitFilesystemFenceIntent(ctx context.Context, runsDir, topology, observationID string, d controlplane.IntentDispositionDecision, key ed25519.PublicKey, load func() (controlplane.Run, CommandRecord, CommandRecord, error), persist func(controlplane.Run, CommandRecord) error) (FenceIntentReconciliation, error) {
	var result FenceIntentReconciliation
	backend, ok := r.backend.(*fileBackend)
	if !ok {
		return result, ErrUnsupportedVersion
	}
	if topology != d.Topology || !topologyName.MatchString(observationID) {
		return result, ErrConflict
	}
	records := &fileGuestRecords{root: runsDir}
	err := records.updateRecord(ctx, executionAuthorityKind, "authority", func(authority []byte) ([]byte, error) {
		ledger, err := decodeExecutionAuthority(authority)
		if err != nil {
			return nil, err
		}
		f, ok := ledger.Fences[d.FenceID]
		if !ok {
			return nil, ErrNotFound
		}
		if rec := f.IntentDisposition; rec != nil {
			if evidenceHash(rec.Decision) != evidenceHash(d) || rec.Observation.Command.ID != observationID {
				return nil, ErrConflict
			}
			if !rec.ReleasedAt.IsZero() {
				result = *rec
				return nil, nil
			}
		}
		run, original, observation, err := load()
		if err != nil {
			return nil, err
		}
		if observation.Command.ID != observationID {
			return nil, ErrConflict
		}
		err = r.update(ctx, topology, func(a *aggregate) error {
			raw, err := json.Marshal(a)
			if err != nil {
				return err
			}
			transition, err := PrepareFenceIntent(authority, raw, d, run, original, observation, key, time.Now().UTC())
			if err != nil {
				return err
			}
			dir, err := records.directory(executionAuthorityKind)
			if err != nil {
				return err
			}
			authorityPath := filepath.Join(dir, "authority.json")
			if err = writePrivateGuestRecord(ctx, authorityPath, transition.Authority); err != nil {
				return err
			}
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
			final, err := FinishFenceIntent(transition.Authority, transition.Lifecycle, transition.Run, transition.Command, d)
			if err != nil {
				return err
			}
			if err = writePrivateGuestRecord(ctx, authorityPath, final); err != nil {
				return err
			}
			completed, err := decodeExecutionAuthority(final)
			if err != nil {
				return err
			}
			result = *completed.Fences[d.FenceID].IntentDisposition
			return nil
		})
		return nil, err
	})
	return result, err
}
