package main

import (
	"fmt"
	"os"

	"github.com/oslab/sysbox/cmd/sysbox/commands"

	docker "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/substrate"
)

func main() {
	dockerSub, err := docker.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker substrate unavailable: %v\n", err)
	} else {
		substrate.Register(dockerSub)
	}

	if err := commands.Execute(); err != nil {
		os.Exit(1)
	}
}
