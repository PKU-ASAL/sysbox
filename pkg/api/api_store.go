package api

import (
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func markInterruptedRuns(runs []controlplane.Run) []controlplane.Run {
	for i := range runs {
		// Restarting an API process is not evidence that an independently
		// assigned agent stopped. Its durable lease and observations decide that.
		if runs[i].AgentID == "" && (runs[i].Status == controlplane.RunAssigned || runs[i].Status == controlplane.RunRunning) {
			runs[i].Status = controlplane.RunFailed
			runs[i].Err = "server restarted before run completion"
			runs[i].Recoverable = true
			if runs[i].EndedAt.IsZero() {
				runs[i].EndedAt = time.Now().UTC()
			}
		}
	}
	return runs
}
