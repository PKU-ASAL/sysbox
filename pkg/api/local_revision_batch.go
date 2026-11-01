package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/revisiongate"
)

func (s *Server) deleteLocalRevisionBatch(ctx context.Context, ids []string, policy RevisionRetentionPolicy) error {
	store, ok := s.apiStore.(*controlstore.Local)
	if !ok || s.stateBackend != "" || len(ids) < 1 || len(ids) > 128 || len(ids) > policy.Limit || policy.KeepLast < 0 || policy.KeepLast > 10000 || policy.OlderThan.IsZero() || policy.OlderThan.After(time.Now()) {
		return persistence.ErrInvalidRecord
	}
	root, err := revisiongate.Catalog(store.RunsDir)
	if err != nil {
		return err
	}
	release, err := revisiongate.Lock(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	// Only raw catalog readers under this lock: never acquire Run/lifecycle locks
	// whose publishers acquire the catalog lock in the opposite order.
	revisions, err := s.globalRevisionInventoryWithFileLock(ctx, false)
	if err != nil {
		return err
	}
	sort.Slice(revisions, func(i, j int) bool {
		if revisions[i].CreatedAt.Equal(revisions[j].CreatedAt) {
			return revisions[i].Revision < revisions[j].Revision
		}
		return revisions[i].CreatedAt.After(revisions[j].CreatedAt)
	})
	keep := map[string]bool{}
	for i, rev := range revisions {
		if i < policy.KeepLast {
			keep[rev.Revision] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !controlstore.GlobalRevisionPattern.MatchString(id) || seen[id] || keep[id] {
			return persistence.ErrConflict
		}
		seen[id] = true
		if err = store.ValidateLocalRevisionRetirement(ctx, root, id, policy.OlderThan); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = s.retireSQLConsumers(ctx, root, ids, policy.OlderThan); err != nil {
		return err
	}
	if err = revisiongate.RetireBatch(root, ids); err != nil {
		return err
	}
	// The immutable proof already hides all members and blocks all late writers.
	// A cleanup failure leaves bytes, never a partially admitted batch. Retry is
	// explicit cleanup of retired payloads, not replay of the old retention plan.
	for _, id := range ids {
		if err = os.Remove(filepath.Join(root, id+".json")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("batch retired; payload cleanup pending: %w", err)
		}
	}
	return controlstore.SyncDirectory(root)
}

// Cleanup only reclaims objects already hidden by a permanent retirement proof.
// It never authorizes a new retirement or removes the admission evidence.
func (s *Server) cleanupLocalRevisionPayloads(ctx context.Context) (int, error) {
	store, ok := s.apiStore.(*controlstore.Local)
	if !ok || s.stateBackend != "" {
		return 0, persistence.ErrUnsupportedVersion
	}
	root, err := revisiongate.Catalog(store.RunsDir)
	if err != nil {
		return 0, err
	}
	release, err := revisiongate.Lock(ctx, root)
	if err != nil {
		return 0, err
	}
	defer release()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	targets := []string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !controlstore.GlobalRevisionPattern.MatchString(id) {
			continue
		}
		retired, err := revisiongate.Retired(root, id)
		if err != nil {
			return 0, err
		}
		if !retired {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			return 0, persistence.ErrCorruptRecord
		}
		targets = append(targets, filepath.Join(root, entry.Name()))
	}
	for i, path := range targets {
		if err = ctx.Err(); err != nil {
			return i, err
		}
		if err = os.Remove(path); err != nil {
			return i, err
		}
	}
	return len(targets), controlstore.SyncDirectory(root)
}
func (s *Server) handleRevisionRetiredCleanup(w http.ResponseWriter, r *http.Request) {
	count, err := s.cleanupLocalRevisionPayloads(r.Context())
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"reclaimed_objects": count, "retirement_proofs_preserved": true})
}
