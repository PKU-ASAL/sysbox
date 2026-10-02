package transport

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

func TestBackgroundShellCommandPreservesEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("POSIX nohup and sh required")
	}
	output := filepath.Join(t.TempDir(), "env value")
	req := substrate.ExecRequest{
		Program:     "/bin/sh",
		Args:        []string{"-c", "printf '%s' \"$CYBERFIELD_TEST_VALUE\" > " + util.ShellQuote(output)},
		Environment: map[string]string{"CYBERFIELD_TEST_VALUE": "a b'c"},
		Shell:       substrate.ShellNone,
	}
	shell, err := backgroundShellCommand(req)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", shell).CombinedOutput(); err != nil {
		t.Fatalf("background shell failed: %v: %s", err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(output)
		if err == nil {
			if string(content) != "a b'c" {
				t.Fatalf("environment was not forwarded: %q", content)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background command did not write the output file")
}
