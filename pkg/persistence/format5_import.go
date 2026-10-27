package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/runtime"
	"io"
)

// Format5ImportPlan uses the same archive-and-compare contract as format 4.
type Format5ImportPlan = Format4ImportPlan

type format5Aggregate struct {
	format4Aggregate
	ResetContinuations map[string]runtime.ResetContinuationReceipt `json:"reset_continuations"`
}

func decodeFormat5(raw []byte) (*aggregate, error) {
	var old format5Aggregate
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrCorruptRecord
	}
	if old.Format != 5 {
		return nil, ErrUnsupportedVersion
	}
	if old.ResetContinuations == nil {
		return nil, ErrCorruptRecord
	}
	return &aggregate{Format: old.Format, Serial: old.Serial, State: old.State, Actions: old.Actions, Snapshots: old.Snapshots, Restores: old.Restores, UpdatedAt: old.UpdatedAt, ResetContinuations: old.ResetContinuations}, nil
}

func PrepareFormat5Import(raw []byte, topology string) (*Format5ImportPlan, error) {
	return prepareLegacyImport(raw, topology, decodeFormat5)
}
func (r *Repository) PlanFormat5Import(ctx context.Context, topology string) (*Format5ImportPlan, error) {
	return r.planLegacyImport(ctx, topology, decodeFormat5)
}
func (r *Repository) ImportFormat5(ctx context.Context, topology, expectedSHA256, archive string) (*Format5ImportPlan, error) {
	return r.importLegacy(ctx, topology, expectedSHA256, archive, decodeFormat5)
}
