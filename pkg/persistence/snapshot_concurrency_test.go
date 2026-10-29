package persistence

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestConcurrentSnapshotNeverSplitsStateAndJournalAcrossBackends(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			repo := resetTestRepository(t, kind)
			ctx := context.Background()
			_, err := repo.LoadState(ctx, "lab")
			require.NoError(t, err)
			errors := make(chan error, 2)
			var workers sync.WaitGroup
			workers.Add(2)
			go func() {
				defer workers.Done()
				for i := 1; i <= 12; i++ {
					err := repo.update(ctx, "lab", func(a *aggregate) error {
						addr := address.Resource("sysbox_node", fmt.Sprintf("node%d", i))
						resource := state.Resource{Address: addr, Driver: "docker", ExternalID: fmt.Sprint(i), OwnerUID: a.State.TopologyUID, ResourceUID: fmt.Sprint(i), Generation: 1}
						a.State.AddResource(resource)
						a.Serial++
						action := runtime.ActionRecord{ID: fmt.Sprintf("action%d", i), Topology: "lab", RunID: "run", Resource: addr, Action: model.PlanActionCreate, Status: runtime.ActionSucceeded, OwnerUID: a.State.TopologyUID, ResourceUID: resource.ResourceUID, Generation: 1, Revision: 1, StateSerial: a.Serial - 1, ExpectedSerial: a.Serial - 1, Candidate: &resource, EffectComplete: true}
						a.Actions[action.ID] = action
						return nil
					})
					if err != nil {
						errors <- err
						return
					}
				}
				errors <- nil
			}()
			go func() {
				defer workers.Done()
				for range 12 {
					if _, err := repo.StateBackend("lab").(state.SnapshotBackend).Snapshot(ctx, "racing writer"); err != nil {
						errors <- err
						return
					}
				}
				errors <- nil
			}()
			workers.Wait()
			require.NoError(t, <-errors)
			require.NoError(t, <-errors)
			require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
				if len(a.Snapshots) != 12 {
					return fmt.Errorf("snapshot count %d", len(a.Snapshots))
				}
				for id, snapshot := range a.Snapshots {
					captured, err := decodeSnapshot(id, snapshot)
					if err != nil {
						return err
					}
					if len(captured.State.Resources) != len(captured.Actions) || int64(len(captured.Actions)) != captured.Serial {
						return fmt.Errorf("snapshot split at %d", captured.Serial)
					}
					for _, action := range captured.Actions {
						resource := captured.State.FindResource(action.Resource)
						if resource == nil || resource.ResourceUID != action.ResourceUID {
							return fmt.Errorf("snapshot omitted action resource")
						}
					}
				}
				return nil
			}))
		})
	}
}
