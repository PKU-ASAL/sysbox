package commands

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/spf13/cobra"
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
	resourceAddress, err := address.Parse(addr)
	if err != nil {
		return err
	}
	if resourceAddress.Type != "sysbox_node" {
		return fmt.Errorf("pause/resume only supported for sysbox_node, got %q", resourceAddress.Type)
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	r := s.FindResource(resourceAddress)
	if r == nil {
		return fmt.Errorf("resource %s not found in state", resourceAddress)
	}

	subName := r.Driver
	powerDriver, err := driver.DefaultRegistry.RequirePower(subName)
	if err != nil {
		return err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(subName)
	if err != nil {
		return err
	}
	handle, err := r.ReconstructHandle(stateDriver)
	if err != nil {
		return err
	}

	ctx := context.Background()
	op := "pause"
	if resume {
		op = "resume"
	}

	if resume {
		err = powerDriver.Resume(ctx, handle)
	} else {
		err = powerDriver.Pause(ctx, handle)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, resourceAddress, err)
	}
	fmt.Printf("%sed %s\n", op, resourceAddress)
	return nil
}
