package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// CopyToNode copies the local file at srcPath into the container at dstPath.
// dstPath must be an absolute path inside the container; the filename is
// preserved from srcPath if dstPath ends with "/".
func (s *Substrate) CopyToNode(ctx context.Context, h substrate.NodeHandle, srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read src %s: %w", srcPath, err)
	}

	// If destination is a directory path, use source filename.
	dstDir := dstPath
	dstFile := filepath.Base(srcPath)
	if !filepath.IsAbs(dstPath) {
		dstDir = "/"
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: dstFile,
		Mode: 0o755,
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	tw.Close()

	return s.cli.CopyToContainer(ctx, h.ID, dstDir, &buf, container.CopyToContainerOptions{
		AllowOverwriteDirWithFile: true,
	})
}

func (s *Substrate) CopyFromNode(_ context.Context, _ substrate.NodeHandle, _, _ string) error {
	return fmt.Errorf("CopyFromNode: not implemented in Phase 2")
}

func (s *Substrate) AttachTTY(_ context.Context, _ substrate.NodeHandle) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("AttachTTY: not implemented in Phase 1")
}

// NodeStatus reports true when the container is in the running state.
func (s *Substrate) NodeStatus(ctx context.Context, h substrate.NodeHandle) (bool, error) {
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return false, nil // container gone
	}
	return ins.State.Running, nil
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
