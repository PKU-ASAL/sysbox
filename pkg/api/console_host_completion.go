package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/persistence"
)

// A signed host result is persisted before releasing the node's execution
// scope. Lost acknowledgements replay the same receipt, never start a console.
func (s *Server) completeConsoleFromHost(ctx context.Context, cmd controlplane.AgentCommand, receipt controlplane.SignedExecutionStopReceipt) error {
	task, err := guesttask.FromConsoleCommand(cmd)
	if err != nil {
		return err
	}
	digest, err := task.Digest()
	if err != nil {
		return err
	}
	g := receipt.Grant
	if receipt.TaskID != task.ID || receipt.TaskDigest != digest || g.Digest != digest || g.ActionID != task.ID || g.AgentID != cmd.AgentID || g.CommandID != cmd.ID || g.Scope != (controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}) {
		return persistence.ErrConflict
	}
	if receipt.Quiescence != "confirmed" || receipt.GuestResult == nil {
		return persistence.ErrConflict
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	repo := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if err = repo.ValidateHostStop(ctx, g, hash); err != nil {
		return err
	}
	if _, err = s.consoles.Snapshot(cmd.Session.ID); err != nil {
		return err
	}
	if err = s.consoles.Update(cmd.Session.ID, func(sess *controlplane.ConsoleSession) {
		if sess.HostCompletion != "" {
			return
		}
		if persistence.ValidateConsoleSessionCAS(*cmd.Session, *sess) != nil {
			return
		}
		sess.HostCompletion = hash
		sess.TerminationConfirmed = true
		sess.EndedAt = receipt.StoppedAt
		if sess.EndedAt.Before(sess.CreatedAt) {
			sess.EndedAt = sess.CreatedAt
		}
		sess.Err = ""
		sess.ExitCode = nil
		result := receipt.GuestResult
		if result.ExitCodeKnown {
			code := result.Result.ExitCode
			sess.ExitCode = &code
			sess.Status = controlplane.ConsoleSessionStatusClosed
		} else if result.ResultClass == "cancelled" {
			sess.Status = controlplane.ConsoleSessionStatusCancelled
		} else {
			sess.Status = controlplane.ConsoleSessionStatusFailed
			sess.Err = "processes stopped; exit status unavailable"
		}
		if result.Result.Truncated {
			sess.Status = controlplane.ConsoleSessionStatusFailed
			sess.Err = "console output limit exceeded"
		}
	}); err != nil {
		return err
	}
	observed, err := s.consoles.Snapshot(cmd.Session.ID)
	if err != nil {
		return err
	}
	if observed.HostCompletion != hash || !observed.TerminationConfirmed {
		return fmt.Errorf("console completion conflicts with persisted session: %w", persistence.ErrConflict)
	}
	if err = repo.AcceptHostStop(ctx, g, hash); err != nil {
		return err
	}
	return retireConsoleDeliveries(ctx, s.apiStore, observed)
}
