package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteBackend_LoadSaveAndVersionedCAS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	backend := &SQLiteBackend{Path: path, Topology: "test"}

	ctx := context.Background()

	// Check that no state exists yet.
	loaded, err := backend.LoadVersioned(ctx)
	if err != nil {
		t.Fatalf("LoadVersioned error: %v", err)
	}
	if loaded.Exists {
		t.Fatal("expected no state, but got one")
	}

	// Save initial state.
	data := []byte(`{"version":2,"resources":[]}`)
	if err := backend.Save(ctx, data); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Load it back.
	loaded, err = backend.LoadVersioned(ctx)
	if err != nil {
		t.Fatalf("LoadVersioned error: %v", err)
	}
	if !loaded.Exists {
		t.Fatal("expected state to exist")
	}
	if loaded.Serial != 1 {
		t.Fatalf("expected serial 1, got %d", loaded.Serial)
	}

	// CAS save with correct serial.
	data2 := []byte(`{"version":2,"resources":[{"type":"a","name":"b"}]}`)
	err = backend.SaveVersioned(ctx, data2, SaveOptions{RequireCAS: true, ExpectedSerial: 1})
	if err != nil {
		t.Fatalf("CAS save error: %v", err)
	}

	// CAS save with wrong serial should fail.
	err = backend.SaveVersioned(ctx, data2, SaveOptions{RequireCAS: true, ExpectedSerial: 1}) // still 1, but should be 2 now
	if err == nil {
		t.Fatal("expected ConflictError, got nil")
	}
	if _, ok := err.(*ConflictError); !ok {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}

	// Lock and unlock.
	unlock, err := backend.Lock(ctx)
	if err != nil {
		t.Fatalf("Lock error: %v", err)
	}
	unlock()

	// Snapshot.
	snap, err := backend.Snapshot(ctx, "test-snap")
	if err != nil {
		t.Fatalf("Snapshot error: %v", err)
	}
	if snap.Reason != "test-snap" {
		t.Fatalf("expected reason 'test-snap', got %q", snap.Reason)
	}

	// List snapshots.
	snaps, err := backend.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots error: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}

	// Restore snapshot.
	if err := backend.RestoreSnapshot(ctx, snap.ID); err != nil {
		t.Fatalf("RestoreSnapshot error: %v", err)
	}

	// Delete.
	if err := backend.Delete(ctx); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	// After delete, should be gone.
	loaded, err = backend.LoadVersioned(ctx)
	if err != nil {
		t.Fatalf("LoadVersioned (after delete) error: %v", err)
	}
	if loaded.Exists {
		t.Fatal("expected no state after delete")
	}

	// Metadata.
	meta, err := backend.Metadata(ctx)
	if err != nil {
		t.Fatalf("Metadata error: %v", err)
	}
	if meta.Backend != "sqlite" {
		t.Fatalf("expected backend=sqlite, got %s", meta.Backend)
	}

	// LockInfo.
	info, err := backend.LockInfo(ctx)
	if err != nil {
		t.Fatalf("LockInfo error: %v", err)
	}
	if info.Locked {
		t.Fatal("expected not locked")
	}

	// ForceUnlock.
	if err := backend.ForceUnlock(ctx); err != nil {
		t.Fatalf("ForceUnlock error: %v", err)
	}

	_ = os.Remove(path)
}
