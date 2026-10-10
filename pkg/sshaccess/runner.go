package sshaccess

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const sshdOutputLimit = 1 << 20
const sshdProbeTimeout = 5 * time.Second

// NewSSHDRunner executes an administrator-selected absolute sshd binary without
// a shell or PATH lookup. Each invocation is bounded independently of the caller
// and accepts only config-validation modes, never daemon startup or reload.
func NewSSHDRunner(binary string) (SSHDRunner, error) {
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || strings.ContainsRune(binary, 0) {
		return nil, fmt.Errorf("trusted absolute sshd executable required")
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		if (len(args) != 3 || args[0] != "-t") && (len(args) != 5 || args[0] != "-T" || args[3] != "-C") {
			return nil, fmt.Errorf("invalid sshd validation arguments")
		}
		if args[1] != "-f" || !filepath.IsAbs(args[2]) || filepath.Clean(args[2]) != args[2] || strings.ContainsRune(args[2], 0) {
			return nil, fmt.Errorf("invalid sshd validation arguments")
		}
		call, cancel := context.WithTimeout(ctx, sshdProbeTimeout)
		defer cancel()
		output := &sshdOutput{cancel: cancel}
		cmd := exec.CommandContext(call, binary, args...)
		cmd.Stdout = sshdStream{output: output, stdout: true}
		cmd.Stderr = sshdStream{output: output}
		// Bound inherited pipe handles even if an unexpected child outlives
		// the trusted sshd process. Never wait indefinitely for pipe EOF.
		cmd.WaitDelay = 100 * time.Millisecond
		err := cmd.Run()
		if output.exceeded {
			return nil, fmt.Errorf("sshd validation output limit exceeded")
		}
		if call.Err() != nil {
			return nil, call.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("sshd validation failed: %w", err)
		}
		return output.stdout.Bytes(), nil
	}, nil
}

type sshdOutput struct {
	mu       sync.Mutex
	stdout   bytes.Buffer
	size     int
	exceeded bool
	cancel   context.CancelFunc
}

type sshdStream struct {
	output *sshdOutput
	stdout bool
}

func (s sshdStream) Write(raw []byte) (int, error) {
	s.output.mu.Lock()
	defer s.output.mu.Unlock()
	if len(raw) > sshdOutputLimit-s.output.size {
		s.output.exceeded = true
		s.output.cancel()
		return len(raw), nil
	}
	s.output.size += len(raw)
	if s.stdout {
		_, _ = s.output.stdout.Write(raw)
	}
	return len(raw), nil
}
