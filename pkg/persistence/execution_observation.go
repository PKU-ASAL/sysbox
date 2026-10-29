package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type ExecutionObservation struct {
	AgentID    string          `json:"agent_id"`
	SessionID  string          `json:"session_id"`
	Kind       string          `json:"kind"`
	Sequence   uint64          `json:"sequence"`
	RecordedAt time.Time       `json:"recorded_at"`
	Data       json.RawMessage `json:"data"`
}

func observationKey(agent, kind string) string {
	raw, _ := json.Marshal([]string{agent, kind})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *ExecutionGrantRepository) PublishObservation(ctx context.Context, agent, session, kind string, sequence uint64, data json.RawMessage) error {
	return r.PublishObservations(ctx, []ExecutionObservation{{AgentID: agent, SessionID: session, Kind: kind, Sequence: sequence, Data: data}})
}

// PublishObservations commits one host snapshot atomically, including inventory
// and every topology projection. A partial response can safely replay it.
func (r *ExecutionGrantRepository) PublishObservations(ctx context.Context, records []ExecutionObservation) error {
	if len(records) == 0 {
		return ErrInvalidRecord
	}
	return r.update(ctx, func(a *executionAuthority) error {
		if a.Observations == nil {
			a.Observations = map[string]ExecutionObservation{}
		}
		seen := map[string]bool{}
		for _, record := range records {
			if record.AgentID == "" || record.SessionID == "" || record.Kind == "" || record.Sequence == 0 || !json.Valid(record.Data) || len(record.Data) > 8<<20 {
				return ErrInvalidRecord
			}
			if a.Sessions[record.AgentID].ID != record.SessionID {
				return ErrConflict
			}
			key := observationKey(record.AgentID, record.Kind)
			if seen[key] {
				return ErrInvalidRecord
			}
			seen[key] = true
			if current, exists := a.Observations[key]; exists && current.SessionID == record.SessionID {
				if record.Sequence < current.Sequence {
					return ErrConflict
				}
				if record.Sequence == current.Sequence {
					if string(current.Data) != string(record.Data) {
						return ErrConflict
					}
					continue
				}
			}
			record.RecordedAt = a.ObservedAt
			record.Data = append(json.RawMessage(nil), record.Data...)
			a.Observations[key] = record
		}
		return nil
	})
}

func (r *ExecutionGrantRepository) ReadObservation(ctx context.Context, agent, kind string) (*ExecutionObservation, error) {
	var result ExecutionObservation
	err := r.read(ctx, func(a *executionAuthority) error {
		record, exists := a.Observations[observationKey(agent, kind)]
		if !exists {
			return ErrNotFound
		}
		if record.AgentID != agent || record.Kind != kind || record.Sequence == 0 || !json.Valid(record.Data) {
			return ErrCorruptRecord
		}
		if a.Sessions[agent].ID != record.SessionID {
			return fmt.Errorf("%w: observation belongs to retired session", ErrConflict)
		}
		result = record
		result.Data = append(json.RawMessage(nil), record.Data...)
		return nil
	})
	return &result, err
}

func (r *ExecutionGrantRepository) ListObservations(ctx context.Context, kind string) ([]ExecutionObservation, error) {
	out := []ExecutionObservation{}
	err := r.read(ctx, func(a *executionAuthority) error {
		for key, record := range a.Observations {
			if key != observationKey(record.AgentID, record.Kind) || record.Sequence == 0 || !json.Valid(record.Data) {
				return ErrCorruptRecord
			}
			if record.Kind != kind || a.Sessions[record.AgentID].ID != record.SessionID {
				continue
			}
			record.Data = append(json.RawMessage(nil), record.Data...)
			out = append(out, record)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, err
}
