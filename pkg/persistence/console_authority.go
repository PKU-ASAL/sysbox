package persistence

import (
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// PrepareConsoleAuthority runs with the session and grant ledger locked. A
// cancelled/lost source cannot receive further grants. Retained grant history
// distinguishes a session that never started from one needing a host stop.
func PrepareConsoleAuthority(raw []byte, sess *controlplane.ConsoleSession) ([]byte, error) {
	if sess == nil || sess.CommandID == "" || sess.TopologyUID == "" || sess.ResourceUID == "" || sess.Generation < 1 || sess.HostCompletion != "" || sess.NotStarted || sess.Status != controlplane.ConsoleSessionStatusCancelling && sess.Status != controlplane.ConsoleSessionStatusLost {
		return raw, nil
	}
	a, err := decodeExecutionAuthority(raw)
	if err != nil {
		return nil, err
	}
	if now := time.Now().UTC(); now.After(a.ObservedAt) {
		a.ObservedAt = now
	}
	if !a.hasCommandExecutionHistory(sess.AgentID, sess.CommandID) {
		sess.NotStarted = true
		sess.EndedAt = a.ObservedAt
		if sess.EndedAt.Before(sess.CreatedAt) {
			sess.EndedAt = sess.CreatedAt
		}
		sess.ExitCode = nil
		if sess.Status == controlplane.ConsoleSessionStatusCancelling {
			sess.Status = controlplane.ConsoleSessionStatusCancelled
			sess.Err = "cancelled before execution authorization"
		} else {
			sess.Status = controlplane.ConsoleSessionStatusFailed
			sess.Err = "session ended before execution authorization"
		}
	} else {
		for key, g := range a.Grants {
			if g.AgentID == sess.AgentID && g.CommandID == sess.CommandID && g.State != controlplane.GrantStopped {
				g.State = controlplane.GrantRevoked
				a.Grants[key] = g
			}
		}
	}
	return json.Marshal(a)
}
