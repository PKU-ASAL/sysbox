package exec

import (
	"context"
	"fmt"
	"os"

	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/substrate"
)

// DockerConnection implements Connection using docker exec / docker cp.
// It wraps the existing docker substrate methods so provisioners use the same
// Docker API client that creates and manages nodes.
type DockerConnection struct {
	sub    *dockerprovider.Substrate
	handle substrate.NodeHandle
}

// NewDockerConnection creates a connection for the given container handle.
func NewDockerConnection(sub *dockerprovider.Substrate, handle substrate.NodeHandle) *DockerConnection {
	return &DockerConnection{sub: sub, handle: handle}
}

func (c *DockerConnection) ExecInline(ctx context.Context, cmds []string) error {
	for _, cmd := range cmds {
		result, err := c.sub.ExecInNode(ctx, c.handle, substrate.ExecSpec{
			Cmd: []string{"sh", "-c", cmd},
		})
		if err != nil {
			return fmt.Errorf("exec %q: %w", cmd, err)
		}
		if result.Stdout != "" {
			fmt.Print(result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(os.Stderr, result.Stderr)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("exec %q: exit code %d", cmd, result.ExitCode)
		}
	}
	return nil
}

func (c *DockerConnection) ExecBackground(ctx context.Context, cmd []string, env map[string]string) (int, error) {
	pid, err := c.sub.ExecBackground(ctx, c.handle, substrate.ExecSpec{
		Cmd: cmd,
		Env: env,
	})
	if err != nil {
		return 0, fmt.Errorf("exec background %v: %w", cmd, err)
	}
	return pid, nil
}

func (c *DockerConnection) CopyFile(ctx context.Context, srcPath, dstPath string) error {
	return c.sub.CopyToNode(ctx, c.handle, srcPath, dstPath)
}
