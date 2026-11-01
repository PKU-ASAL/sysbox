package controlplane

import (
	"strings"
)

func NormalizeRun(run *Run) {
	if run.Operation == "" {
		run.Operation = run.Op
	}
	if run.Op == "" {
		run.Op = run.Operation
	}
	if run.ProjectID == "" {
		run.ProjectID = DefaultProjectID
	}
	if run.Workspace == "" {
		run.Workspace = run.Topology
	}
}

// SameRunIncarnation compares the immutable execution binding, not progress
// timestamps or renewable lease fields. Inputs may only be reused within it.
func SameRunIncarnation(a, b Run) bool {
	NormalizeRun(&a)
	NormalizeRun(&b)
	if runRequestBinding(a.RequestFingerprint) != runRequestBinding(b.RequestFingerprint) {
		return false
	}
	return a.ID == b.ID && a.Topology == b.Topology && a.ProjectID == b.ProjectID && a.Workspace == b.Workspace && a.Operation == b.Operation && a.Op == b.Op && a.AgentID == b.AgentID && a.Revision == b.Revision && a.SnapshotPath == b.SnapshotPath && a.OperationKey == b.OperationKey && a.Target == b.Target && a.ParentID == b.ParentID && a.UnsafeState == b.UnsafeState && a.QueuedAt.Equal(b.QueuedAt) && a.DeadlineAt.Equal(b.DeadlineAt)
}

// Assignment markers describe the dispatch transition, not client authorization.
// Request fingerprints, in contrast, are immutable input-hydration authority.
func runRequestBinding(fingerprint string) string {
	if strings.HasPrefix(fingerprint, "run-dispatch:") {
		return ""
	}
	return fingerprint
}
