package persistence

import (
	"context"
	"encoding/json"
	"regexp"
)

var retainedRevisionDigest = regexp.MustCompile(`sha256:[a-f0-9]{64}`)

// LoadRevisionReferences includes historical action candidates and decoded
// snapshots, never treating encoded snapshot bytes as an empty reference set.
// Unresolved or corrupt lifecycle evidence rejects the entire observation.
func (r *Repository) LoadRevisionReferences(ctx context.Context, topology string) (map[string]bool, error) {
	out := map[string]bool{}
	err := r.update(ctx, topology, func(a *aggregate) error {
		if _, err := gcReferences(a); err != nil {
			return err
		}
		var collect func(*aggregate) error
		collect = func(a *aggregate) error {
			copy := *a
			copy.Snapshots = nil
			raw, err := json.Marshal(copy)
			if err != nil {
				return err
			}
			for _, digest := range retainedRevisionDigest.FindAllString(string(raw), -1) {
				out[digest] = true
			}
			for id, snapshot := range a.Snapshots {
				content, err := decodeSnapshot(id, snapshot)
				if err != nil {
					return err
				}
				if err = collect(content); err != nil {
					return err
				}
			}
			return nil
		}
		return collect(a)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
