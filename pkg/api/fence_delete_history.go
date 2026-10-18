package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Reconciliation must not leave a torn JSONL suffix that would prevent WAL
// recovery. Keep the stable history lock but publish a complete fsynced inode.
func (s *localAPIStore) saveFenceDeleteRun(ctx context.Context, run controlplane.Run) error {
	if validatePathSegment(run.Topology, "topology") != nil || validatePathSegment(run.ID, "run") != nil {
		return persistence.ErrInvalidRecord
	}
	path := filepath.Join(s.runsDir, run.Topology, "runs.jsonl")
	lock, err := lockLocalHistory(ctx, path, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return errCorruptLocalRecord
	}
	for _, line := range bytes.Split(raw[:len(raw)-1], []byte{'\n'}) {
		var existing controlplane.Run
		if decodeLocalRecord(line, &existing) != nil {
			return errCorruptLocalRecord
		}
	}
	encoded, err := json.Marshal(persistence.DurableRun(run))
	if err != nil {
		return err
	}
	payload := append(append(raw, encoded...), byte('\n'))
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
	return syncDirectory(filepath.Dir(path))
}
