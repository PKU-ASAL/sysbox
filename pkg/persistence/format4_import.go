package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// Format4ImportPlan describes a one-time data conversion, not execution
// authority. SourceSHA256 is compared again under the aggregate write lock.
type Format4ImportPlan struct {
	SourceSHA256 string `json:"source_sha256"`
	TopologyUID  string `json:"topology_uid"`
	Serial       int64  `json:"serial"`
	Actions      int    `json:"actions"`
	Snapshots    int    `json:"snapshots"`
	Converted    []byte `json:"-"`
}

// The old representation is confined to this explicit importer. Normal reads
// continue to require FormatVersion and never invoke this decoder.
type format4Aggregate struct {
	Restores  map[string]SnapshotRestoreReceipt `json:"restores"`
	Snapshots map[string]aggregateSnapshot      `json:"snapshots"`
	Format    int                               `json:"format"`
	Serial    int64                             `json:"serial"`
	State     *state.State                      `json:"state"`
	Actions   map[string]runtime.ActionRecord   `json:"actions"`
	UpdatedAt time.Time                         `json:"updated_at"`
}

func decodeFormat4(raw []byte) (*aggregate, error) {
	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	if header.Format != 4 {
		return nil, ErrUnsupportedVersion
	}
	var old format4Aggregate
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrCorruptRecord
	}
	return &aggregate{Format: old.Format, Serial: old.Serial, State: old.State, Actions: old.Actions, Snapshots: old.Snapshots, Restores: old.Restores, UpdatedAt: old.UpdatedAt}, nil
}

func convertFormat4(raw []byte, topology string, snapshot bool) (*aggregate, error) {
	return convertLegacyAggregate(raw, topology, snapshot, decodeFormat4)
}

func convertLegacyAggregate(raw []byte, topology string, snapshot bool, decode func([]byte) (*aggregate, error)) (*aggregate, error) {
	a, err := decode(raw)
	if err != nil {
		return nil, err
	}
	a.GC = map[string]GCReservation{}
	a.Format = FormatVersion
	if a.ResetContinuations == nil {
		a.ResetContinuations = map[string]runtime.ResetContinuationReceipt{}
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, err
	}
	if snapshot && (len(a.Snapshots) != 0 || len(a.Restores) != 0) {
		return nil, ErrCorruptRecord
	}
	for id, snap := range a.Snapshots {
		if id == "" || id != snap.Metadata.ID || snap.Metadata.CreatedAt.IsZero() || snap.Metadata.Size != int64(len(snap.Data)) || snap.Digest != fmt.Sprintf("%x", sha256.Sum256(snap.Data)) {
			return nil, ErrCorruptRecord
		}
		inner, err := convertLegacyAggregate(snap.Data, topology, true, decode)
		if err != nil {
			return nil, err
		}
		if inner.State.TopologyUID != snap.TopologyUID || inner.Serial != snap.Serial {
			return nil, ErrCorruptRecord
		}
		snap.Data, err = json.Marshal(inner)
		if err != nil {
			return nil, err
		}
		snap.Digest = fmt.Sprintf("%x", sha256.Sum256(snap.Data))
		snap.Metadata.Size = int64(len(snap.Data))
		a.Snapshots[id] = snap
	}
	return a, nil
}

func PrepareFormat4Import(raw []byte, topology string) (*Format4ImportPlan, error) {
	return prepareLegacyImport(raw, topology, decodeFormat4)
}
func prepareLegacyImport(raw []byte, topology string, decode func([]byte) (*aggregate, error)) (*Format4ImportPlan, error) {
	if !topologyName.MatchString(topology) {
		return nil, ErrInvalidRecord
	}
	a, err := convertLegacyAggregate(raw, topology, false, decode)
	if err != nil {
		return nil, err
	}
	converted, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return &Format4ImportPlan{SourceSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), TopologyUID: a.State.TopologyUID, Serial: a.Serial, Actions: len(a.Actions), Snapshots: len(a.Snapshots), Converted: converted}, nil
}

func (r *Repository) PlanFormat4Import(ctx context.Context, topology string) (*Format4ImportPlan, error) {
	return r.planLegacyImport(ctx, topology, decodeFormat4)
}
func (r *Repository) planLegacyImport(ctx context.Context, topology string, decode func([]byte) (*aggregate, error)) (*Format4ImportPlan, error) {
	if !topologyName.MatchString(topology) || r.boundTopology != "" && r.boundTopology != topology {
		return nil, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var raw []byte
	var err error
	switch b := r.backend.(type) {
	case *fileBackend:
		raw, err = os.ReadFile(filepath.Join(b.root, topology+".json"))
	case *sqlBackend:
		query := `SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=?`
		if b.postgres {
			query = `SELECT data FROM sysbox_lifecycle_aggregates WHERE topology=$1`
		}
		err = b.db.QueryRowContext(ctx, query, topology).Scan(&raw)
	default:
		return nil, ErrUnsupportedVersion
	}
	if err != nil {
		return nil, err
	}
	return prepareLegacyImport(raw, topology, decode)
}

// ImportFormat4 performs only a verified metadata conversion. Administrators
// must stop old writers before use; the execution lock cannot exclude old
// binaries. This function does not prove provider quiescence or authorize resume.
// The exact old record (including original snapshot bytes/restore receipts) is
// durably archived before replacing it. A failed commit may leave that archive.
func (r *Repository) ImportFormat4(ctx context.Context, topology, expectedSHA256, archive string) (*Format4ImportPlan, error) {
	return r.importLegacy(ctx, topology, expectedSHA256, archive, decodeFormat4)
}
func (r *Repository) importLegacy(ctx context.Context, topology, expectedSHA256, archive string, decodeOld func([]byte) (*aggregate, error)) (*Format4ImportPlan, error) {
	if archive == "" {
		return nil, ErrInvalidRecord
	}
	unlock, err := r.LockExecution(ctx, topology)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if b, ok := r.backend.(*fileBackend); ok {
		if filepath.Ext(archive) == ".json" {
			root, err := os.Stat(b.root)
			if err != nil {
				return nil, err
			}
			if parent, err := os.Stat(filepath.Dir(archive)); err == nil && os.SameFile(root, parent) {
				return nil, ErrConflict // do not introduce a format4 record into the topology catalog
			}
		}
		source, err := os.Stat(filepath.Join(b.root, topology+".json"))
		if err != nil {
			return nil, err
		}
		if target, err := os.Stat(archive); err == nil && os.SameFile(source, target) {
			return nil, ErrConflict
		}
	}
	var source []byte
	var plan *Format4ImportPlan
	decode := func(raw []byte) (*aggregate, error) {
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != expectedSHA256 {
			return nil, ErrConflict
		}
		source = append([]byte(nil), raw...)
		return decodeOld(raw)
	}
	commit := func(a *aggregate) error {
		if len(source) == 0 {
			return ErrNotFound
		}
		var err error
		plan, err = prepareLegacyImport(source, topology, decodeOld)
		if err != nil {
			return err
		}
		next, err := decodeAggregate(plan.Converted)
		if err != nil {
			return err
		}
		if err := archiveImportSource(archive, source); err != nil {
			return err
		}
		*a = *next
		return nil
	}
	switch b := r.backend.(type) {
	case *fileBackend:
		err = b.updateDecoded(ctx, topology, decode, commit)
	case *sqlBackend:
		err = b.updateDecoded(ctx, topology, decode, commit)
	default:
		err = ErrUnsupportedVersion
	}
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func archiveImportSource(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".import-archive-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrConflict
		}
		prior, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(prior, raw) {
			return ErrConflict
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
