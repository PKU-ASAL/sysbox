package agentexec

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

// The declared timeout must cover the whole guest operation, not just the exec
// call at the end of it.
//
// The branch reports the execution as started, resolves a state manager and
// loads state before any deadline exists. A slow or wedged state backend
// therefore burns unbounded time before the budget is even created, so the
// latency an operator observes stays unbounded no matter how well the exec path
// itself honours the deadline.
func TestGuestOperationContextBoundsTheWholeOperation(t *testing.T) {
	ctx, cancel := guestOperationContext(context.Background(), controlplane.GuestExecutionRequest{TimeoutSeconds: 1})
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok, "a declared timeout must produce a deadline before any work begins")
	require.WithinDuration(t, time.Now().Add(time.Second), deadline, 250*time.Millisecond)
}

// No declared timeout means the operation inherits whatever bound its caller
// has; it must not invent one.
func TestGuestOperationContextLeavesUndeclaredTimeoutAlone(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	ctx, cancel := guestOperationContext(parent, controlplane.GuestExecutionRequest{})
	defer cancel()

	_, ok := ctx.Deadline()
	require.False(t, ok, "an undeclared timeout must not be given one here")

	// Cancellation must still flow from the parent.
	parentCancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach the derived context")
	}
}

// A parent deadline that is tighter than the declared one must win: the caller's
// bound is an upper limit, not a suggestion.
func TestGuestOperationContextKeepsTighterParentDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer parentCancel()

	ctx, cancel := guestOperationContext(parent, controlplane.GuestExecutionRequest{TimeoutSeconds: 3600})
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the parent's tighter deadline was not honoured")
	}
}
