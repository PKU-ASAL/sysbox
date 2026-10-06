package api

import (
	"crypto/sha256"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"strings"
	"time"
)

func runCommandID(run controlplane.Run) string {
	sum := sha256.Sum256([]byte(run.ID + "\x00" + run.QueuedAt.UTC().Format(time.RFC3339Nano) + "\x00" + run.Revision))
	return fmt.Sprintf("run-%x", sum)
}
func runAssignmentFingerprint(run controlplane.Run) string {
	return "run-dispatch:" + runCommandID(run) + ":" + run.Op + ":" + run.AgentID
}
func runDispatchKey(request RunDispatchRequest) string {
	if strings.HasPrefix(request.Fingerprint, "run-dispatch:") {
		return runCommandID(request.Run)
	}
	return request.Run.ID // HTTP idempotency key mapping remains stable.
}
func validateDispatchReplay(current controlplane.Run, request RunDispatchRequest) error {
	if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && !persistence.SameRunIncarnation(current, request.Run) {
		return persistence.ErrConflict
	}
	return nil
}

func validateRunDispatchPayload(request RunDispatchRequest) error {
	if !strings.HasPrefix(request.Fingerprint, "run-dispatch:") {
		return nil
	}
	cmd := request.Command
	if cmd.Run == nil || cmd.Type != "run_assigned" || cmd.AgentID != request.Run.AgentID || !persistence.SameRunIncarnation(*cmd.Run, request.Run) {
		return persistence.ErrConflict
	}
	return nil
}
