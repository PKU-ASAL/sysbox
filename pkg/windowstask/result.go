package windowstask

import "github.com/oslab/sysbox/pkg/taskprotocol"

// A guard can finalize unknown termination before the supervisor observes its
// process wait. That terminal evidence must remain immutable for host receipts.
func mergeWorkerResult(current, completed taskprotocol.Record) taskprotocol.Record {
	if current.TerminationConfirmed {
		return current
	}
	return completed
}
