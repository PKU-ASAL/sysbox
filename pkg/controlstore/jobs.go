package controlstore

import (
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func NormalizeRunProductFields(r *controlplane.Run) {
	if r == nil {
		return
	}
	controlplane.NormalizeRun(r)
}

func LatestRunsByID(runs []controlplane.Run) []controlplane.Run {
	latest := map[string]controlplane.Run{}
	for _, run := range runs {
		prev, ok := latest[run.ID]
		if !ok || run.QueuedAt.After(prev.QueuedAt) || run.QueuedAt.Equal(prev.QueuedAt) && (RunStatusRank(run.Status) >= RunStatusRank(prev.Status) || RunRecordTime(run).After(RunRecordTime(prev))) {
			latest[run.ID] = run
		}
	}
	out := make([]controlplane.Run, 0, len(latest))
	for _, run := range latest {
		out = append(out, run)
	}
	return out
}

func RunStatusRank(status controlplane.RunStatus) int {
	return status.Rank()
}

func RunRecordTime(run controlplane.Run) time.Time {
	switch {
	case !run.EndedAt.IsZero():
		return run.EndedAt
	case !run.StartedAt.IsZero():
		return run.StartedAt
	case !run.AssignedAt.IsZero():
		return run.AssignedAt
	default:
		return run.QueuedAt
	}
}
