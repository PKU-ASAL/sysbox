package controlstore

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

var ErrActiveOperation = fmt.Errorf("%w: topology has an active operation", persistence.ErrConflict)

func ValidateTopologyAdmission(runs []controlplane.Run, next controlplane.Run) error {
	if !next.Status.IsActive() {
		return nil
	}
	for _, run := range LatestRunsByID(runs) {
		if run.Topology == next.Topology && run.ID != next.ID && run.Status.IsActive() {
			return ErrActiveOperation
		}
	}
	return nil
}
