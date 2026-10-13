package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProveRunStopped is read-only. Its caller holds Run execution exclusion so
// admission cannot race the scan. Missing/corrupt inventories remain unknown.
func (d *Dispatcher) ProveRunStopped(ctx context.Context, runID string) error {
	if d == nil || runID == "" || d.AgentID == "" || len(d.HostKey) != ed25519.PrivateKeySize || len(d.IssuerKey) != ed25519.PublicKeySize {
		return fmt.Errorf("Run task inventory unavailable")
	}
	if err := ValidateOwnedPath(d.Root, false); err != nil {
		return err
	}
	entries, err := os.ReadDir(d.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(d.Root, entry.Name()))
		if err != nil {
			return err
		}
		var record TaskRecord
		if json.Unmarshal(raw, &record) != nil || recordKey(record.Handle)+".json" != entry.Name() {
			return fmt.Errorf("invalid Run task inventory record")
		}
		if record.RunID != runID {
			continue
		}
		unlock, err := d.lock(ctx, record.Handle)
		if err != nil {
			return err
		}
		current, err := d.load(record.Handle)
		unlock()
		if err != nil {
			return err
		}
		if current.RunID != runID || current.StopReceipt == nil || !current.Reported {
			return fmt.Errorf("Run child termination unconfirmed")
		}
	}
	return ctx.Err()
}
