package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type CacheGCDisposition struct {
	Plan   CacheGCPlan `json:"plan"`
	Action string      `json:"action"`
	Reason string      `json:"reason"`
}

func exactCacheRecord(path string, plan CacheGCPlan) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Nlink != 1 || info.Size() > 65536 {
		return ErrConflict
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var saved CacheGCPlan
	if json.Unmarshal(raw, &saved) != nil || saved != plan {
		return ErrConflict
	}
	return nil
}
func (r *Repository) cacheDispositionBinding(ctx context.Context, plan CacheGCPlan) error {
	b, ok := r.backend.(*sqlBackend)
	if !ok || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	if !b.postgres {
		return nil
	}
	host, err := cacheHostID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	var matches bool
	err = b.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sysbox_cache_deletions WHERE id=$1 AND host=$2 AND plan=$3::jsonb)", plan.ID, host, string(raw)).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrConflict
	}
	return nil
}
func exactCacheTarget(plan CacheGCPlan) error {
	current, err := cachePlan(nil, plan.CatalogSHA256, plan.Root, plan.Path)
	if err != nil {
		return err
	}
	if current != plan {
		return ErrConflict
	}
	return nil
}
func inspectCacheDisposition(plan CacheGCPlan, journal string) CacheGCDisposition {
	out := CacheGCDisposition{Plan: plan, Action: "manual_review", Reason: "preserve reservation and evidence; verify original host, journal integrity and target identity before operator disposition"}
	seen := 0
	for _, ext := range []string{".intent", ".done", ".aborted"} {
		_, err := os.Lstat(filepath.Join(journal, plan.ID+ext))
		if err == nil {
			seen++
		} else if !os.IsNotExist(err) {
			return out
		}
	}
	if seen > 1 {
		return out
	}
	intent := filepath.Join(journal, plan.ID+".intent")
	if _, err := os.Lstat(filepath.Join(journal, plan.ID+".aborted")); err == nil {
		if exactCacheRecord(filepath.Join(journal, plan.ID+".aborted"), plan) != nil {
			return out
		}
		if _, err = os.Lstat(intent); os.IsNotExist(err) && exactCacheTarget(plan) == nil {
			out.Action = "abort"
			out.Reason = "exact aborted receipt; only reservation release remains"
		}
		return out
	}
	if _, err := os.Lstat(filepath.Join(journal, plan.ID+".done")); err == nil {
		if verifyCacheDone(plan) != nil {
			return out
		}
		out.Action = "reconcile"
		out.Reason = "exact completion receipt and absent target"
		return out
	}
	if err := exactCacheRecord(intent, plan); err != nil {
		out.Reason = "missing or invalid intent: " + err.Error()
		return out
	}
	if _, err := os.Lstat(filepath.Join(plan.Root, plan.Path)); os.IsNotExist(err) {
		out.Action = "recover"
		out.Reason = "exact intent, original deletion lock available, target absent"
		return out
	}
	if exactCacheTarget(plan) == nil {
		out.Action = "abort"
		out.Reason = "original deleter stopped; exact original target remains; preserve file and retire this deletion"
	}
	return out
}

// Inspection takes the same lock but never changes journal or database state.
func (r *Repository) InspectCacheGC(ctx context.Context, plan CacheGCPlan) (CacheGCDisposition, error) {
	journal, err := cacheRecoveryPaths(plan)
	if err != nil {
		return CacheGCDisposition{Plan: plan, Action: "manual_review", Reason: err.Error()}, err
	}
	lock := flock.New(filepath.Join(journal, "lock"))
	defer lock.Close()
	held, err := lock.TryLock()
	if err != nil {
		return CacheGCDisposition{}, err
	}
	if !held {
		return CacheGCDisposition{Plan: plan, Action: "wait", Reason: "original deletion lock is held"}, nil
	}
	if err = r.cacheDispositionBinding(ctx, plan); err != nil {
		return CacheGCDisposition{Plan: plan, Action: "manual_review", Reason: err.Error()}, err
	}
	report := inspectCacheDisposition(plan, journal)
	if r.backend.(*sqlBackend).postgres && report.Action == "manual_review" && exactCacheTarget(plan) == nil && r.cacheReservationPending(ctx, plan) {
		missing := true
		for _, ext := range []string{".intent", ".done", ".aborted"} {
			if _, err := os.Lstat(filepath.Join(journal, plan.ID+ext)); !os.IsNotExist(err) {
				missing = false
			}
		}
		if missing {
			report.Action = "abort"
			report.Reason = "durable original-host reservation before intent; exact original target remains"
		}
	}
	return report, nil
}

// Abort never removes the target. The exact original file and durable intent
// plus the exclusive host lock prove this deleter cannot subsequently unlink.
func (r *Repository) AbortCacheGC(ctx context.Context, plan CacheGCPlan) error {
	journal, err := cacheRecoveryPaths(plan)
	if err != nil {
		return err
	}
	lock := flock.New(filepath.Join(journal, "lock"))
	defer lock.Close()
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !held {
		return ErrConflict
	}
	if err = r.cacheDispositionBinding(ctx, plan); err != nil {
		return err
	}
	report := inspectCacheDisposition(plan, journal)
	b := r.backend.(*sqlBackend)
	missingIntent := false
	if b.postgres && report.Action == "manual_review" && exactCacheTarget(plan) == nil && r.cacheReservationPending(ctx, plan) {
		missingIntent = true
		for _, ext := range []string{".intent", ".done", ".aborted"} {
			if _, err := os.Lstat(filepath.Join(journal, plan.ID+ext)); !os.IsNotExist(err) {
				missingIntent = false
			}
		}
		if missingIntent {
			report.Action = "abort"
		}
	}
	if report.Action != "abort" {
		return fmt.Errorf("%w: %s", ErrConflict, report.Reason)
	}
	aborted := filepath.Join(journal, plan.ID+".aborted")
	if _, err = os.Lstat(aborted); os.IsNotExist(err) {
		if err = ctx.Err(); err != nil {
			return err
		}
		if missingIntent {
			raw, _ := json.Marshal(plan)
			f, err := os.OpenFile(aborted, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = f.Write(raw)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		} else if err = os.Rename(filepath.Join(journal, plan.ID+".intent"), aborted); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err = syncCacheDir(plan.Root); err != nil {
		return err
	}
	if err = syncCacheDir(journal); err != nil {
		return err
	}
	if !b.postgres {
		return nil
	}
	// Preserve the disposition in plan-independent audit storage; 'done' on the
	// reservation means terminal (unfrozen), not successful resource deletion.
	host, err := cacheHostID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, "ALTER TABLE sysbox_cache_deletions ADD COLUMN IF NOT EXISTS outcome TEXT")
	if err != nil {
		return err
	}
	result, err := b.db.ExecContext(ctx, "UPDATE sysbox_cache_deletions SET state='done',outcome='aborted' WHERE id=$1 AND host=$2 AND plan=$3::jsonb AND (state='pending' OR outcome='aborted')", plan.ID, host, string(raw))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) cacheReservationPending(ctx context.Context, plan CacheGCPlan) bool {
	b := r.backend.(*sqlBackend)
	var pending bool
	err := b.db.QueryRowContext(ctx, "SELECT state='pending' FROM sysbox_cache_deletions WHERE id=$1", plan.ID).Scan(&pending)
	return err == nil && pending
}
