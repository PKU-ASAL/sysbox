package commands

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/substrate"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <resource_type.name>",
	Short: "Pause a running node (freeze VM or container)",
	Args:  cobra.ExactArgs(1),
	RunE:  runPause,
}

var resumeCmd = &cobra.Command{
	Use:   "resume <resource_type.name>",
	Short: "Resume a paused node",
	Args:  cobra.ExactArgs(1),
	RunE:  runResume,
}

func runPause(cmd *cobra.Command, args []string) error {
	return pauseResumeOp(args[0], false)
}

func runResume(cmd *cobra.Command, args []string) error {
	return pauseResumeOp(args[0], true)
}

func pauseResumeOp(addr string, resume bool) error {
	typ, name, err := splitAddr(addr)
	if err != nil {
		return err
	}
	if typ != "sysbox_node" {
		return fmt.Errorf("pause/resume only supported for sysbox_node, got %q", typ)
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	r := s.FindResource(address.Resource(typ, name))
	if r == nil {
		return fmt.Errorf("resource %s.%s not found in state", typ, name)
	}

	subName := r.Driver
	sub, err := substrate.Get(subName)
	if err != nil {
		return fmt.Errorf("substrate %q: %w", subName, err)
	}

	handle := substrate.NodeHandle{ID: r.Str("container_id")}
	if handle.ID == "" {
		handle.ID = name
	}

	// Reconstruct provider state if available.
	if extra := r.Str("provider_extra"); extra != "" {
		if v, err := sub.UnmarshalProviderState([]byte(extra)); err == nil {
			handle.Provider = v
		}
	}

	ctx := context.Background()
	op := "pause"
	if resume {
		op = "resume"
	}

	caps := sub.Capabilities()
	if !caps.SupportsPause {
		return fmt.Errorf("substrate %q does not support pause/resume", subName)
	}

	if resume {
		err = sub.Resume(ctx, handle)
	} else {
		err = sub.Pause(ctx, handle)
	}
	if err != nil {
		return fmt.Errorf("%s %s.%s: %w", op, typ, name, err)
	}
	fmt.Printf("%sed %s.%s\n", op, typ, name)
	return nil
}
