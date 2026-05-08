package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/oslab/sysbox/cmd/sysbox/commands"
	docker "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/substrate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dockerSub, err := docker.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker substrate unavailable: %v\n", err)
	} else {
		substrate.Register(dockerSub)
	}

	if err := commands.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
