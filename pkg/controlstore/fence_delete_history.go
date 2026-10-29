package controlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/revisiongate"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Reconciliation must not leave a torn JSONL suffix that would prevent WAL
// recovery. Keep the stable history lock but publish a complete fsynced inode.
func (s *Local) SaveFenceDeleteRun(ctx context.Context, run controlplane.Run) error {
	if ValidatePathSegment(run.Topology, "topology") != nil || ValidatePathSegment(run.ID, "run") != nil {
		return persistence.ErrInvalidRecord
	}
	path := filepath.Join(s.RunsDir, run.Topology, "runs.jsonl")
	lock, err := LockLocalHistory(ctx, path, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return ErrCorruptLocalRecord
	}
	for _, line := range bytes.Split(raw[:len(raw)-1], []byte{'\n'}) {
		var existing controlplane.Run
		if DecodeLocalRecord(line, &existing) != nil {
			return ErrCorruptLocalRecord
		}
	}
	encoded, err := json.Marshal(persistence.DurableRun(run))
	if err != nil {
		return err
	}
	payload := append(append(raw, encoded...), byte('\n'))
	release, err := revisiongate.Guard(ctx, path, encoded)
	if err != nil {
		return err
	}
	defer release()
	file, err := os.CreateTemp(filepath.Dir(path), ".fence-run-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	n, err := file.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return SyncDirectory(filepath.Dir(path))
}
