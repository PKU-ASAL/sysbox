package agentexec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// A run whose checkpoint journal could not be persisted must not be reported as
// successful. The journal is what makes a crashed or partial run reconcilable
// afterwards — replaying it is how resources created but never recorded in
// state are found again — so "succeeded, but unjournalled" is a claim sysbox
// cannot actually stand behind.
func TestRunOutcomeFailsSuccessfulRunWithBrokenJournal(t *testing.T) {
	journalErr := errors.New("persist checkpoint for run r1: backend unavailable")

	err := runOutcome(nil, journalErr)

	require.Error(t, err)
	require.ErrorIs(t, err, journalErr)
}

// The real failure must survive: a journal problem discovered alongside a
// genuine apply error must not displace the apply error, or the operator would
// be told about bookkeeping instead of about what actually broke.
func TestRunOutcomeKeepsTheRealFailure(t *testing.T) {
	applyErr := errors.New("create sysbox_node.web: no such image")
	journalErr := errors.New("persist checkpoint: backend unavailable")

	err := runOutcome(applyErr, journalErr)

	require.ErrorIs(t, err, applyErr)
}

// A healthy run stays healthy.
func TestRunOutcomeIsCleanWhenBothAreFine(t *testing.T) {
	require.NoError(t, runOutcome(nil, nil))
}
