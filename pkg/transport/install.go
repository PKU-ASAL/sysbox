package transport

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
	"path/filepath"
)

// InstallFile preserves the verified copier contract while applying an explicit
// requested mode. Unknown completion is an error; its unique staging path is
// retained for inspection rather than guessing which effects completed.
func InstallFile(ctx context.Context, conn substrate.Connection, src, dst string, mode uint32) error {
	files, ok := conn.(substrate.ConnectionFileCopier)
	if !ok {
		return substrate.ErrNotSupported
	}
	if mode & ^uint32(0777) != 0 {
		return fmt.Errorf("invalid file permission mode")
	}
	stage := dst + ".sysbox-install-" + rand.Text()
	prepared, err := conn.Exec(ctx, substrate.ExecRequest{Program: "mkdir", Args: []string{"-m", "0700", "--", stage}, Shell: substrate.ShellNone}, nil, nil)
	if err != nil {
		return fmt.Errorf("prepare private guest file staging: %w", err)
	}
	if !prepared.ExitCodeKnown || prepared.ExitCode != 0 {
		return fmt.Errorf("private guest file staging did not return a known successful result")
	}
	temporary := filepath.Join(stage, "payload")
	if err := files.CopyFile(ctx, src, temporary); err != nil {
		return err
	}
	path := util.ShellQuote(temporary)
	script := fmt.Sprintf("set -eu; chmod %04o -- %s; sync -f %s; mv -fT -- %s %s; rmdir -- %s; sync -f %s", mode, path, path, path, util.ShellQuote(dst), util.ShellQuote(stage), util.ShellQuote(filepath.Dir(dst)))
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: script, Shell: substrate.ShellLinux}, nil, nil)
	if err != nil {
		return fmt.Errorf("install guest file: %w", err)
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 {
		return fmt.Errorf("guest file installation did not return a known successful result (known=%t exit=%d): %s", result.ExitCodeKnown, result.ExitCode, result.Stderr)
	}
	return nil
}
