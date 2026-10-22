package guesttask

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestFileFenceAcceptanceProbe is a disposable-VM test entrypoint, never a
// production helper flag. It pauses the real installer after fsync/prepared.
func TestFileFenceAcceptanceProbe(t *testing.T) {
	root := os.Getenv("SYSBOX_FILE_FENCE_ROOT")
	if root == "" {
		t.Skip("requires disposable file fencing harness")
	}
	key, err := os.ReadFile(filepath.Join(root, "issuer.pub"))
	if err != nil {
		os.Exit(2)
	}
	call, err := ReadCall(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	e := Engine{Root: filepath.Join(root, "journal"), PublicKey: key, AllowedInstallRoots: []string{filepath.Join(root, "target")}}
	e.installHook = func(phase string) error {
		if phase == "prepared" {
			if err := os.WriteFile(filepath.Join(root, "ready"), []byte("prepared"), 0600); err != nil {
				return err
			}
			select {}
		}
		return nil
	}
	response := e.Handle(context.Background(), call)
	if json.NewEncoder(os.Stdout).Encode(response) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
