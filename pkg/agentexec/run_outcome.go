package agentexec

import "fmt"

// runOutcome folds a checkpoint-journal failure into a run's outcome.
//
// The journal is the only thing that makes a crashed or partial run
// reconcilable afterwards: replaying it is how resources that were created but
// never recorded in state are found again. A run that completes on a journal
// that stopped being written looks exactly like a healthy one, and the gap is
// discovered only on the day a recovery is attempted — so an unjournalled run
// is reported as failed rather than certified as successful.
//
// A genuine failure always wins: the operator needs to hear what actually
// broke, not about the bookkeeping that also broke.
func runOutcome(err, journalErr error) error {
	if err != nil {
		return err
	}
	if journalErr != nil {
		return fmt.Errorf("run cannot be certified, its checkpoint journal is incomplete: %w", journalErr)
	}
	return nil
}
