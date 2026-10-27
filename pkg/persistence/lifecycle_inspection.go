package persistence

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

// LifecycleInspection reads state and journal under one aggregate lock. It is
// recorded evidence, not a live provider observation or mutation authorization.
type LifecycleInspection struct {
	State       *state.State
	Actions     []runtime.ActionRecord
	StateExists bool
}

func (r *Repository) InspectLifecycle(ctx context.Context, topology string) (*LifecycleInspection, error) {
	out := &LifecycleInspection{Actions: []runtime.ActionRecord{}}
	err := r.update(ctx, topology, func(a *aggregate) error {
		var err error
		out.State, err = cloneState(a.State)
		if err != nil {
			return err
		}
		out.StateExists = a.Serial > 0
		out.State.Meta = state.StateMeta{Backend: r.backend.name(), Serial: a.Serial, Exists: out.StateExists, UpdatedAt: a.UpdatedAt}
		// Detach candidates/private maps as well as the outer slice from storage.
		raw, err := json.Marshal(a.Actions)
		if err != nil {
			return err
		}
		var actions map[string]runtime.ActionRecord
		if err = json.Unmarshal(raw, &actions); err != nil {
			return err
		}
		for _, action := range actions {
			out.Actions = append(out.Actions, action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out.Actions, func(i, j int) bool { return out.Actions[i].ID < out.Actions[j].ID })
	return out, nil
}
