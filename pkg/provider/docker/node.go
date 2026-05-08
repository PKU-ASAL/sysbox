package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	envs := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envs = append(envs, fmt.Sprintf("%s=%s", k, v))
	}

	resp, err := s.cli.ContainerCreate(ctx,
		&container.Config{
			Image: spec.Image.Repository,
			Env:   envs,
			Cmd:   []string{"sleep", "infinity"},
		},
		&container.HostConfig{
			NetworkMode: "none",
			CapAdd:      []string{"NET_ADMIN"},
		},
		&network.NetworkingConfig{},
		nil,
		spec.Name,
	)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("create container: %w", err)
	}

	return substrate.NodeHandle{
		ID: resp.ID,
		Attributes: map[string]any{
			"container_name": spec.Name,
		},
	}, nil
}

func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerStart(ctx, h.ID, container.StartOptions{})
}

func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	timeoutSec := 10
	return s.cli.ContainerStop(ctx, h.ID, container.StopOptions{Timeout: &timeoutSec})
}

func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true})
}
