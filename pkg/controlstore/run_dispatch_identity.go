package controlstore

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

func RunCommandID(run controlplane.Run) string {
	sum := sha256.Sum256([]byte(run.ID + "\x00" + run.QueuedAt.UTC().Format(time.RFC3339Nano) + "\x00" + run.Revision))
	return fmt.Sprintf("run-%x", sum)
}

func RunDispatchKey(request RunDispatchRequest) string {
	if strings.HasPrefix(request.Fingerprint, "run-dispatch:") {
		return RunCommandID(request.Run)
	}
	return request.Run.ID // HTTP idempotency key mapping remains stable.
}

func validateDispatchReplay(current controlplane.Run, request RunDispatchRequest) error {
	if strings.HasPrefix(request.Fingerprint, "run-dispatch:") && !controlplane.SameRunIncarnation(current, request.Run) {
		return persistence.ErrConflict
	}
	return nil
}

// DispatchIdentityConflict reports whether binding this dispatch to the run
// would reuse an idempotency identity for a different request. A run admitted
// through a durable request keeps that request fingerprint when it is assigned
// to an agent: the run-dispatch marker names the command, not a second request,
// so assigning or re-dispatching a restarted incarnation does not replace the
// run's immutable input-hydration authority.
func DispatchIdentityConflict(current, requested string) bool {
	if current == "" || current == requested {
		return false
	}
	return !(strings.HasPrefix(requested, "run-dispatch:") && !strings.HasPrefix(current, "run-dispatch:"))
}

func ValidateRunDispatchPayload(request RunDispatchRequest) error {
	if request.Run.Cleanup != nil || request.Run.HostCompletion != nil || request.Command.Run != nil && (request.Command.Run.HostCompletion != nil || request.Command.Run.Cleanup != nil) {
		return persistence.ErrConflict
	}
	if !strings.HasPrefix(request.Fingerprint, "run-dispatch:") {
		return nil
	}
	cmd := request.Command
	if cmd.Run == nil || cmd.Type != "run_assigned" || cmd.AgentID != request.Run.AgentID || !controlplane.SameRunIncarnation(*cmd.Run, request.Run) {
		return persistence.ErrConflict
	}
	return nil
}
