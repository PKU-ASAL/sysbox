package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) ExecInNode(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	envs := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envs = append(envs, fmt.Sprintf("%s=%s", k, v))
	}

	ex, err := s.cli.ContainerExecCreate(ctx, h.ID, container.ExecOptions{
		Cmd:          spec.Cmd,
		Env:          envs,
		WorkingDir:   spec.WorkDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec create: %w", err)
	}

	att, err := s.cli.ContainerExecAttach(ctx, ex.ID, container.ExecStartOptions{})
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec read: %w", err)
	}

	inspect, err := s.cli.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec inspect: %w", err)
	}

	return substrate.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: inspect.ExitCode,
	}, nil
}

func (s *Substrate) CopyToNode(_ context.Context, _ substrate.NodeHandle, _, _ string) error {
	return fmt.Errorf("CopyToNode: not implemented in Phase 1")
}

func (s *Substrate) CopyFromNode(_ context.Context, _ substrate.NodeHandle, _, _ string) error {
	return fmt.Errorf("CopyFromNode: not implemented in Phase 1")
}

func (s *Substrate) AttachTTY(_ context.Context, _ substrate.NodeHandle) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("AttachTTY: not implemented in Phase 1")
}

func (s *Substrate) ObservationHook(ctx context.Context, h substrate.NodeHandle) (substrate.ObservationTarget, error) {
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return substrate.ObservationTarget{}, err
	}
	return substrate.ObservationTarget{
		Kind:  "host-pid-namespace",
		Value: fmt.Sprintf("%d", ins.State.Pid),
	}, nil
}
