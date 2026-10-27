// acceptance runs an exact list of real-environment Go tests. A successful Go
// exit alone is insufficient: missing, skipped or unfinished tests fail the gate.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type event struct{ Action, Package, Test, Output string }

func verify(reader io.Reader, expected []string) error {
	decoder := json.NewDecoder(reader)
	running := map[string]bool{}
	passed := map[string]bool{}
	packages := map[string]bool{}
	want := map[string]bool{}
	for _, name := range expected {
		want[name] = true
	}
	for {
		var e event
		if err := decoder.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("invalid go test event stream: %w", err)
		}
		if e.Output != "" {
			fmt.Fprint(os.Stdout, e.Output)
		}
		key := e.Package + "/" + e.Test
		switch e.Action {
		case "run":
			running[key] = true
		case "skip", "fail":
			return fmt.Errorf("acceptance %s: %s", e.Action, key)
		case "pass":
			if e.Test == "" {
				packages[e.Package] = true
				continue
			}
			if !running[key] {
				return fmt.Errorf("test passed without execution: %s", key)
			}
			delete(running, key)
			if want[e.Test] {
				passed[e.Test] = true
			}
		}
	}
	if len(running) > 0 || len(packages) == 0 {
		return fmt.Errorf("incomplete acceptance event stream")
	}
	for _, name := range expected {
		if !passed[name] {
			return fmt.Errorf("required test did not execute and pass: %s", name)
		}
	}
	return nil
}

func run() error {
	config := flag.NewFlagSet("acceptance", flag.ContinueOnError)
	pkg := config.String("package", "", "one Go package")
	tests := config.String("tests", "", "comma-separated required test names")
	timeout := config.String("timeout", "3m", "Go test timeout")
	race := config.Bool("race", false, "enable race detection")
	goBin := config.String("go", "go", "Go executable")
	if err := config.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *pkg == "" || *tests == "" || config.NArg() != 0 {
		return fmt.Errorf("package and exact test names required")
	}
	names := strings.Split(*tests, ",")
	patterns := make([]string, len(names))
	for i, name := range names {
		if !regexp.MustCompile(`^Test[A-Za-z0-9_]+$`).MatchString(name) {
			return fmt.Errorf("invalid test name %q", name)
		}
		patterns[i] = regexp.QuoteMeta(name)
	}
	args := []string{"test", "-json", "-tags=e2e", "-count=1", "-timeout=" + *timeout, "-run=^(" + strings.Join(patterns, "|") + ")$"}
	if *race {
		args = append(args, "-race")
	}
	args = append(args, *pkg)
	cmd := exec.Command(*goBin, args...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	verification := verify(stdout, names)
	// Do not kill the test on a failed assertion: its cleanup owns real
	// resources. Drain the pipe and wait so all registered cleanup can run.
	_, drainErr := io.Copy(os.Stdout, stdout)
	waitErr := cmd.Wait()
	if verification != nil {
		return verification
	}
	if waitErr != nil {
		return waitErr
	}
	return drainErr
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
