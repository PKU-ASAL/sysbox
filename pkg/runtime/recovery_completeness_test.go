package runtime

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// design-principles.md sets a completeness bar: "没有 checkpoint 和幂等恢复的
// provider 不能被视为完整实现."
//
// Nothing checked it. CheckpointRecoverer is satisfied by an optional type
// assertion, so a handler that simply never implemented it is indistinguishable
// from one that genuinely does not need it. As providers accumulate, recovery
// coverage silently falls behind the bar the project set for itself, and the
// gap only surfaces on the day a crashed apply needs reconciling.
//
// This test forces the distinction to be stated: every registered handler must
// either implement CheckpointRecoverer, or declare — with a reason recorded in
// code — why recovery is not required for it.
func TestEveryResourceHandlerStatesItsRecoveryPosition(t *testing.T) {
	var undeclared []string

	for _, typ := range RegisteredResourceTypes() {
		handler, ok := GetResourceHandler(typ)
		require.True(t, ok, "registry listed %q but cannot return it", typ)

		_, recovers := handler.(CheckpointRecoverer)
		waived, waives := handler.(RecoveryNotRequired)

		switch {
		case recovers && waives:
			t.Errorf("%s both implements recovery and waives it; pick one", typ)
		case recovers:
			// Implements idempotent checkpoint recovery.
		case waives:
			require.NotEmpty(t, waived.RecoveryNotRequired(),
				"%s waives recovery without saying why; the reason is the point", typ)
		default:
			undeclared = append(undeclared, typ)
		}
	}

	sort.Strings(undeclared)
	require.Empty(t, undeclared, "these handlers state no recovery position — "+
		"implement CheckpointRecoverer, or declare RecoveryNotRequired with a reason: %v", undeclared)
}

// The waiver must not become a silent dumping ground: reasons are read by
// people deciding whether a crashed apply left something behind, so they have
// to say something specific.
func TestRecoveryWaiversGiveASubstantiveReason(t *testing.T) {
	for _, typ := range RegisteredResourceTypes() {
		handler, _ := GetResourceHandler(typ)
		waived, ok := handler.(RecoveryNotRequired)
		if !ok {
			continue
		}
		reason := waived.RecoveryNotRequired()
		require.GreaterOrEqual(t, len(reason), 20,
			"%s: %q is too terse to justify waiving recovery", typ, reason)
	}
}

// A guard on the registry enumerator itself: if it silently returned nothing,
// both tests above would pass while checking nothing at all.
func TestRegisteredResourceTypesIsNotEmpty(t *testing.T) {
	types := RegisteredResourceTypes()
	require.NotEmpty(t, types)
	require.Contains(t, types, "sysbox_node",
		fmt.Sprintf("expected the node handler to be registered; got %v", types))
}
