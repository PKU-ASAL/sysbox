package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

func taskEngine(operation string) (*guesttask.Engine, error) {
	key, err := os.ReadFile("/etc/sysbox/grant-public-key")
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("install trusted /etc/sysbox/grant-public-key before using guest tasks")
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cgroup := "/sys/fs/cgroup/sysbox-tasks"
	if !strings.HasPrefix(operation, "file-") && !strings.HasPrefix(operation, "ssh-") {
		if err = os.MkdirAll(cgroup, 0700); err != nil {
			return nil, fmt.Errorf("delegate writable cgroup-v2 task root: %w", err)
		}
	}
	engine := &guesttask.Engine{Root: "/var/lib/sysbox/guest-tasks", CgroupRoot: cgroup, PublicKey: ed25519.PublicKey(key), WorkerBinary: binary}
	engine.PolicyRoot = "/var/lib/sysbox/guest-policies"
	if raw, err := os.ReadFile("/etc/sysbox/node-identity.json"); err == nil {
		var node substrate.OperationIdentity
		if err = json.Unmarshal(raw, &node); err != nil || node.Validate() != nil {
			return nil, fmt.Errorf("invalid guest node identity")
		}
		engine.NodeIdentity = &node
	}
	if strings.HasPrefix(operation, "file-") && operation != "file-info" {
		roots, err := loadInstallRoots("/etc/sysbox/install-roots.json")
		if err != nil {
			return nil, err
		}
		engine.AllowedInstallRoots = roots
	}
	return engine, nil
}

func loadInstallRoots(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("trusted install roots required: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("install roots config must be root-owned regular file without group/other write")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("install roots config too large")
	}
	var roots []string
	if err := json.Unmarshal(raw, &roots); err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("install roots must explicitly allow a directory")
	}
	return roots, nil
}

func serveTask(reader io.Reader) guesttask.Response {
	call, err := guesttask.ReadCall(reader)
	if err != nil {
		return guesttask.Response{Version: guesttask.Version, Error: err.Error()}
	}
	engine, err := taskEngine(call.Operation)
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
	case "--guest-policy":
		if err := runPolicyBuiltin(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true

	case "--ssh-service-install", "--ssh-service-recover":
		operation := strings.TrimPrefix(os.Args[1], "--ssh-service-")
		if err := manageSSHService(context.Background(), operation, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	case "--ssh-authorized-keys":
		engine, err := taskEngine("ssh-authorized-keys")
		if err == nil {
			err = writeSSHAuthorizedKeys(context.Background(), engine, os.Args[2:], os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
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

func runPolicyBuiltin() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("guest policy requires the installed privileged worker")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	if err != nil {
		return err
	}
	var request guesttask.Request
	if err = json.Unmarshal(raw, &request); err != nil {
		return err
	}
	engine, err := taskEngine("policy-worker")
	if err != nil {
		return err
	}
	if engine.NodeIdentity == nil {
		return fmt.Errorf("rebuild guest with node identity")
	}
	boot, err := guesttask.BootID()
	if err != nil {
		return err
	}
	result, err := guesttask.ExecutePolicyBuiltin(context.Background(), request, engine.PublicKey, boot, *engine.NodeIdentity, engine.PolicyRoot)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
