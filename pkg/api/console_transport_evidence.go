package api

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"time"
)

// Closing a websocket is not evidence that a remote process stopped. Preserve
// completion already reported by the execution path and retain unknown results.
func recordConsoleTransportLoss(sess *controlplane.ConsoleSession, reason string) {
	switch sess.Status {
	case controlplane.ConsoleSessionStatusClosed, controlplane.ConsoleSessionStatusFailed, controlplane.ConsoleSessionStatusCancelled, controlplane.ConsoleSessionStatusDenied:
		return
	case controlplane.ConsoleSessionStatusCancelling:
		return
	default:
		sess.Status = controlplane.ConsoleSessionStatusLost
		sess.Err = reason
		sess.EndedAt = time.Now().UTC()
	}
}
