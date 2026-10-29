package api

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
)

func runAssignmentFingerprint(run controlplane.Run) string {
	return "run-dispatch:" + controlstore.RunCommandID(run) + ":" + run.Op + ":" + run.AgentID
}
