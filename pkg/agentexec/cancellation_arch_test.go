package agentexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A run's context is the only path by which a deadline, a Ctrl-C or an operator
// cancellation reaches the provider doing the work.
//
// cmd/sysbox/main.go builds a signal-aware context (SIGINT/SIGTERM) and cobra
// carries it to every command via cmd.Context(). Substituting
// context.Background() at the executor boundary throws that away one call short
// of the executor, so `sysbox apply` cannot be interrupted at all: the signal
// arrives, the context it belongs to is never consulted, and provisioning runs
// to completion regardless.
//
// The agent path already passes a cancellable runCtx; this guards the CLI from
// drifting back.
func TestCommandsDoNotDropTheRunContext(t *testing.T) {
	forbidden := []string{
		// The context-less convenience wrapper: it exists only to substitute
		// context.Background() and was removed for that reason.
		".Execute(run)",
		"ExecuteContext(context.Background()",
	}

	require.NoError(t, filepath.WalkDir("../../cmd", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pattern := range forbidden {
			require.NotContains(t, string(data), pattern,
				"%s severs the run's cancellation path; pass cmd.Context() instead", path)
		}
		return nil
	}))
}
