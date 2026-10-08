package agentexec

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
)

type hostRunClient interface {
	StartRun(context.Context, hostsupervisor.RunRequest, controlplane.SignedExecutionGrant) (hostsupervisor.RunObservation, error)
	ObserveRun(context.Context, hostsupervisor.RunRequest, controlplane.SignedExecutionGrant) (hostsupervisor.RunObservation, error)
	RenewRun(context.Context, hostsupervisor.RunRequest, controlplane.SignedExecutionGrant) error
	CancelRun(context.Context, hostsupervisor.RunRequest, controlplane.SignedExecutionGrant) error
}

type renewHostRunGrant func(context.Context, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error)

func terminalHostRun(observation hostsupervisor.RunObservation, original controlplane.Run) (*controlplane.Run, error) {
	if observation.Status != "completed" {
		return nil, fmt.Errorf("host run outcome unknown: status %q", observation.Status)
	}
	run, err := decodeHostRunEvidence(observation.Evidence)
	if err != nil {
		return nil, err
	}
	if !persistence.SameRunIncarnation(original, *run) || run.Attempt != original.Attempt || run.LeaseOwner != original.LeaseOwner || run.AgentID != original.AgentID {
		return nil, fmt.Errorf("host run outcome unknown: terminal evidence identity mismatch")
	}
	return run, nil
}

func awaitHostRun(ctx context.Context, client hostRunClient, req hostsupervisor.RunRequest, original controlplane.Run, grant controlplane.SignedExecutionGrant, key ed25519.PublicKey, renew renewHostRunGrant) (*controlplane.Run, error) {
	initial := grant
	budget, err := controlplane.ExecutionGrantBudget(grant, time.Now())
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(budget)
	nextRenew := time.Now().Add(min(budget/3, 10*time.Second))
	// Cleanup remains available after the run context is canceled. A cancel
	// acknowledgement is never treated as evidence that a run completed.
	fail := func(cause error, reconcile bool) (*controlplane.Run, error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.CancelRun(cleanupCtx, req, grant)
		if reconcile && ctx.Err() == nil {
			observation, observeErr := client.ObserveRun(cleanupCtx, req, grant)
			if observeErr == nil && observation.Status == "completed" {
				if run, evidenceErr := terminalHostRun(observation, original); evidenceErr == nil {
					return run, nil
				}
			}
		}
		return nil, fmt.Errorf("host run outcome unknown: %w", cause)
	}
	startCtx, startCancel := context.WithDeadline(ctx, nextRenew)
	observation, err := client.StartRun(startCtx, req, grant)
	startCancel()
	if err != nil {
		if ctx.Err() != nil {
			return fail(ctx.Err(), false)
		}
		// A lost reply may have followed a successful start. Only observe the
		// original identity; never replay StartRun or mint a replacement run.
		observeCtx, cancel := context.WithDeadline(ctx, deadline)
		observation, err = client.ObserveRun(observeCtx, req, grant)
		cancel()
		if err != nil {
			return fail(err, false)
		}
	}
	for {
		if ctx.Err() != nil {
			return fail(ctx.Err(), false)
		}
		switch observation.Status {
		case "completed":
			run, evidenceErr := terminalHostRun(observation, original)
			if evidenceErr != nil {
				return fail(evidenceErr, false)
			}
			return run, nil
		case "running":
		default:
			return fail(fmt.Errorf("host status %q", observation.Status), false)
		}
		if !time.Now().Before(nextRenew) {
			renewCtx, cancel := context.WithDeadline(ctx, deadline)
			next, renewErr := renew(renewCtx, grant)
			if renewErr == nil {
				renewErr = controlplane.VerifyExecutionGrant(key, next, initial.BootID, time.Now())
			}
			if renewErr == nil {
				identity := next.Grant
				identity.ExpiresAt = initial.Grant.ExpiresAt
				if identity != initial.Grant || !next.Grant.ExpiresAt.After(grant.Grant.ExpiresAt) || next.IssuedAt.Before(grant.IssuedAt) {
					renewErr = controlplane.ErrInvalidExecutionGrant
				}
			}
			if renewErr == nil {
				budget, renewErr = controlplane.ExecutionGrantBudget(next, time.Now())
			}
			// Capture the monotonic budget before sending the renewal to host.
			nextDeadline := time.Now().Add(budget)
			if renewErr == nil {
				renewErr = client.RenewRun(renewCtx, req, next)
			}
			cancel()
			if renewErr != nil {
				return fail(renewErr, true)
			}
			grant = next
			deadline = nextDeadline
			nextRenew = time.Now().Add(min(time.Until(deadline)/3, 10*time.Second))
		}
		wait := time.NewTimer(max(0, min(250*time.Millisecond, time.Until(nextRenew))))
		select {
		case <-ctx.Done():
			wait.Stop()
			return fail(ctx.Err(), false)
		case <-wait.C:
		}
		// Renew first when the poll interval reaches the renewal boundary.
		if !time.Now().Before(nextRenew) {
			continue
		}
		observeCtx, cancel := context.WithDeadline(ctx, nextRenew)
		observation, err = client.ObserveRun(observeCtx, req, grant)
		cancel()
		if err != nil {
			if ctx.Err() == nil && !time.Now().Before(nextRenew) {
				observation.Status = "running"
				continue
			}
			return fail(err, false)
		}
	}
}
