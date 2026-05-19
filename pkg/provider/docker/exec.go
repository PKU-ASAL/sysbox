package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

func (s *Substrate) ExecInNode(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	envs := util.EnvToSlice(spec.Env)

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

	// Resolve destination directory and filename.
	// If dstPath ends with "/" it is a directory; keep source filename.
	// Otherwise treat dstPath as the full destination path: dir + new name.
	var dstDir, dstFile string
	if strings.HasSuffix(dstPath, "/") {
		dstDir = dstPath
		dstFile = filepath.Base(srcPath)
	} else {
		dstDir = filepath.Dir(dstPath)
		dstFile = filepath.Base(dstPath)
	}
	if !filepath.IsAbs(dstDir) {
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

// ExecBackground starts a detached command inside the container and returns
// the PID of the process as seen inside the container namespace.
// The process is not attached to any terminal and survives exec-client disconnect.
func (s *Substrate) ExecBackground(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (int, error) {
	envs := util.EnvToSlice(spec.Env)

	ex, err := s.cli.ContainerExecCreate(ctx, h.ID, container.ExecOptions{
		Cmd:          spec.Cmd,
		Env:          envs,
		WorkingDir:   spec.WorkDir,
		Detach:       true,
		AttachStdout: false,
		AttachStderr: false,
	})
	if err != nil {
		return 0, fmt.Errorf("exec create (background): %w", err)
	}

	if err := s.cli.ContainerExecStart(ctx, ex.ID, container.ExecStartOptions{Detach: true}); err != nil {
		return 0, fmt.Errorf("exec start (background): %w", err)
	}

	// Docker daemon creates the detached exec process asynchronously;
	// an immediate inspect may return Pid=0. Poll until the process
	// is either running with a real PID or has already exited.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		inspect, err := s.cli.ContainerExecInspect(ctx, ex.ID)
		if err != nil {
			return 0, fmt.Errorf("exec inspect (background): %w", err)
		}
		if inspect.Pid != 0 || !inspect.Running {
			return inspect.Pid, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("exec background: timed out waiting for PID")
}

// GetContainerIP returns the container's IP address on its first Docker-managed
// network. Used to construct ACP URLs for actor resources after apply.
func (s *Substrate) GetContainerIP(ctx context.Context, containerID string) (string, error) {
	ins, err := s.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	for _, ep := range ins.NetworkSettings.Networks {
		if ep.IPAddress != "" {
			return ep.IPAddress, nil
		}
	}
	return "", fmt.Errorf("no Docker-network IP found for container %s", containerID)
}

// NodeStatus reports true when the container is in the running state.
func (s *Substrate) NodeStatus(ctx context.Context, h substrate.NodeHandle) (bool, error) {
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return false, nil // container gone
	}
	return ins.State.Running, nil
}
