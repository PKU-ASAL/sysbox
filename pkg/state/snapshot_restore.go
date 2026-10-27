package state

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrSnapshotConflict = errors.New("snapshot restore conflict")

// validateSnapshotIncarnation keeps metadata restore from rolling a canonical
// resource back to an older owner/generation/external identity. Providers must
// run an explicit observed restore/adoption workflow for such changes.
func validateSnapshotIncarnation(currentData, snapshotData []byte) error {
	if len(currentData) == 0 {
		return nil
	}
	var current, snapshot struct {
		TopologyUID string     `json:"topology_uid"`
		Resources   []Resource `json:"resources"`
	}
	if err := json.Unmarshal(currentData, &current); err != nil {
		return err
	}
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		return err
	}
	if current.TopologyUID != "" && snapshot.TopologyUID != current.TopologyUID {
		return fmt.Errorf("%w: snapshot topology identity changed", ErrSnapshotConflict)
	}
	for _, live := range current.Resources {
		var candidate *Resource
		for i := range snapshot.Resources {
			if snapshot.Resources[i].Address.Equal(live.Address) {
				candidate = &snapshot.Resources[i]
				break
			}
		}
		if candidate == nil || candidate.OwnerUID != live.OwnerUID || candidate.ResourceUID != live.ResourceUID || candidate.Generation != live.Generation || candidate.ExternalID != live.ExternalID || candidate.Driver != live.Driver {
			return fmt.Errorf("%w: snapshot resource incarnation changed for %s", ErrSnapshotConflict, live.Address)
		}
	}
	return nil
}
