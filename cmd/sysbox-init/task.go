package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/oslab/sysbox/pkg/guesttask"
)

func taskEngine() (*guesttask.Engine, error) {
	key, err := os.ReadFile("/etc/sysbox/grant-public-key")
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("install trusted /etc/sysbox/grant-public-key before using guest tasks")
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cgroup := "/sys/fs/cgroup/sysbox-tasks"
	if err = os.MkdirAll(cgroup, 0700); err != nil {
		return nil, fmt.Errorf("delegate writable cgroup-v2 task root: %w", err)
	}
	return &guesttask.Engine{Root: "/var/lib/sysbox/guest-tasks", CgroupRoot: cgroup, PublicKey: ed25519.PublicKey(key), WorkerBinary: binary}, nil
}

func serveTask(reader io.Reader) guesttask.Response {
	call, err := guesttask.ReadCall(reader)
	if err != nil {
		return guesttask.Response{Version: guesttask.Version, Error: err.Error()}
	}
	engine, err := taskEngine()
	if err != nil {
		return guesttask.Response{Version: guesttask.Version, Error: err.Error()}
	}
	return engine.Handle(context.Background(), call)
}

func runTaskMode() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "--guest-task":
		_ = json.NewEncoder(os.Stdout).Encode(serveTask(os.Stdin))
		return true
	case "--guest-task-worker":
		if err := guesttask.RunWorker(os.Getenv("SYSBOX_GUESTTASK_ROOT"), os.Getenv("SYSBOX_GUESTTASK_CGROUP"), os.Getenv("SYSBOX_GUESTTASK_ID")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	case "--guest-task-guard":
		if err := guesttask.RunGuard(os.Getenv("SYSBOX_GUESTTASK_ROOT"), os.Getenv("SYSBOX_GUESTTASK_CGROUP"), os.Getenv("SYSBOX_GUESTTASK_ID")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	}
	return false
}
