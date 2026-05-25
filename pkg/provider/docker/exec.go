package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
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

func (s *Substrate) OpenConsole(ctx context.Context, h substrate.NodeHandle, req substrate.ConsoleRequest) (substrate.ConsoleSession, error) {
	cmd := req.Cmd
	if len(cmd) == 0 {
		shell := req.Shell
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = []string{shell}
	}
	ex, err := s.cli.ContainerExecCreate(ctx, h.ID, container.ExecOptions{
		Cmd:          cmd,
		Env:          util.EnvToSlice(req.Env),
		WorkingDir:   req.WorkDir,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          req.TTY,
	})
	if err != nil {
		return nil, fmt.Errorf("exec create console: %w", err)
	}
	att, err := s.cli.ContainerExecAttach(ctx, ex.ID, container.ExecStartOptions{Tty: req.TTY})
	if err != nil {
		return nil, fmt.Errorf("exec attach console: %w", err)
	}
	if req.TTY && req.Cols > 0 && req.Rows > 0 {
		_ = s.cli.ContainerExecResize(ctx, ex.ID, container.ResizeOptions{Width: uint(req.Cols), Height: uint(req.Rows)})
	}
	return &dockerConsoleSession{
		sub:    s,
		execID: ex.ID,
		attach: att,
		tty:    req.TTY,
	}, nil
}

type dockerConsoleSession struct {
	sub    *Substrate
	execID string
	attach dockertypes.HijackedResponse
	tty    bool
}

func (s *dockerConsoleSession) Stdin() io.WriteCloser { return s.attach.Conn }
func (s *dockerConsoleSession) Stdout() io.Reader {
	if s.tty {
		return s.attach.Reader
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, s.attach.Reader)
		_ = pw.CloseWithError(err)
	}()
	return pr
}
func (s *dockerConsoleSession) Stderr() io.Reader { return nil }
func (s *dockerConsoleSession) Resize(ctx context.Context, cols, rows int) error {
	if !s.tty || cols <= 0 || rows <= 0 {
		return nil
	}
	return s.sub.cli.ContainerExecResize(ctx, s.execID, container.ResizeOptions{Width: uint(cols), Height: uint(rows)})
}
func (s *dockerConsoleSession) Wait() (int, error) {
	for {
		ins, err := s.sub.cli.ContainerExecInspect(context.Background(), s.execID)
		if err != nil {
			return -1, err
		}
		if !ins.Running {
			return ins.ExitCode, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func (s *dockerConsoleSession) Close() error {
	s.attach.Close()
	return nil
}

var _ substrate.ConsoleProvider = (*Substrate)(nil)

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
	obs, err := s.ObserveNode(ctx, h)
	if err != nil {
		return false, err
	}
	return obs.Running, nil
}

func (s *Substrate) ObserveNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return substrate.NodeObservation{
				Exists:     false,
				Running:    false,
				Healthy:    false,
				Status:     substrate.NodeStatusMissing,
				ExternalID: h.ID,
				Reason:     "container not found",
				LastSeen:   time.Now().UTC(),
			}, nil
		}
		return substrate.NodeObservation{
			Status:     substrate.NodeStatusUnknown,
			ExternalID: h.ID,
			Reason:     err.Error(),
			LastSeen:   time.Now().UTC(),
		}, err
	}
	status := substrate.NodeStatusExited
	if ins.State.Running {
		status = substrate.NodeStatusRunning
	}
	if ins.State.Paused {
		status = substrate.NodeStatusPaused
	}
	healthy := ins.State.Running
	if ins.State.Health != nil && ins.State.Health.Status == "unhealthy" {
		status = substrate.NodeStatusUnhealthy
		healthy = false
	}
	var exitCode *int
	if !ins.State.Running {
		code := ins.State.ExitCode
		exitCode = &code
	}
	return substrate.NodeObservation{
		Exists:     true,
		Running:    ins.State.Running,
		Healthy:    healthy,
		Status:     status,
		PID:        ins.State.Pid,
		ExitCode:   exitCode,
		ExternalID: ins.ID,
		Reason:     ins.State.Status,
		LastSeen:   time.Now().UTC(),
	}, nil
}
