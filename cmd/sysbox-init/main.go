// sysbox-init is a tiny PID 1 wrapper that runs inside Firecracker microVMs.
//
// Boot flow:
//
//  1. Mount /proc, /sys, /dev (idempotent — kernel may have done some).
//  2. Mount the config drive (/dev/vdb) at /sysbox-config (ext4, read-only).
//  3. Read /sysbox-config/config.json: hostname, authorized_keys, env,
//     vsock_port, chain_init.
//  4. Apply config:
//     - sethostname()
//     - install authorized_keys to /root/.ssh/authorized_keys
//     - drop env into /etc/profile.d/sysbox-env.sh
//  5. Start background vsock-agent (phase D — for now just a stub goroutine).
//  6. exec(chain_init), default /sbin/init, fallback /bin/sh.
//
// The rootfs supplies only the chain_init binary (any standard init or a
// shell). All sysbox-specific config lives on the config drive, so the rootfs
// itself stays generic and pristine across VMs in the same field.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/oslab/sysbox/pkg/vsockrpc"
)

const (
	configDevice = "/dev/vdb"
	configMount  = "/sysbox-config"
	configFile   = "config.json"
)

// VMConfig is the schema written by sysbox onto the config drive.
// Keep in sync with pkg/provider/firecracker/configdrive.go.
type VMConfig struct {
	Hostname       string            `json:"hostname,omitempty"`
	AuthorizedKeys []string          `json:"authorized_keys,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	VsockPort      uint32            `json:"vsock_port,omitempty"`
	ChainInit      string            `json:"chain_init,omitempty"`
}

// defaultPATH is what sysbox-init guarantees to itself and to every child it
// spawns (including the vsock-agent that handles provisioner commands).
//
// The kernel hands PID 1 an empty environment, which means `exec.Command("ip", ...)`
// or `exec.Command("sh", ...)` would fail with "executable file not found in
// $PATH". Setting this once covers alpine (`/sbin`, `/bin`), debian/ubuntu
// (`/usr/sbin`, `/usr/bin`), and locally-installed binaries.
const defaultPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Argv0 selector: when invoked with the hidden "--vsock-agent" first arg,
// the binary runs as a long-lived vsock RPC server (PID 2+, spawned from
// PID 1 mode below). This re-exec pattern keeps the server alive across
// the syscall.Exec that hands PID 1 over to the user's real init.
func main() {
	if os.Getenv("PATH") == "" {
		_ = os.Setenv("PATH", defaultPATH)
	}

	if len(os.Args) >= 2 && os.Args[1] == "--vsock-agent" {
		port := uint32(vsockrpc.DefaultPort)
		if len(os.Args) >= 3 {
			if v, err := strconv.ParseUint(os.Args[2], 10, 32); err == nil {
				port = uint32(v)
			}
		}
		if err := startVsockServer(port); err != nil {
			logf("vsock-agent: %v", err)
			os.Exit(1)
		}
		return
	}

	// --vm-sensor <events_csv> <node_name>
	// Re-exec mode: streams tracefs events to stdout until killed.
	// Invoked by the host-side vm-vsock backend over OpExec.
	if len(os.Args) >= 2 && os.Args[1] == "--vm-sensor" {
		eventsCSV := "execve,fork,exit"
		nodeName := ""
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--events" && i+1 < len(os.Args) {
				eventsCSV = os.Args[i+1]
				i++
			} else if os.Args[i] == "--node" && i+1 < len(os.Args) {
				nodeName = os.Args[i+1]
				i++
			}
		}
		runVMSensor(eventsCSV, nodeName)
		return
	}

	logf("sysbox-init starting (pid %d)", os.Getpid())

	mountEssentials()

	cfg, cfgErr := mountAndReadConfig()
	if cfgErr != nil {
		logf("WARN: config drive unusable (%v) — booting with defaults", cfgErr)
	}

	applyConfig(cfg)

	// Apply any kernel-cmdline `ip=` directives ourselves. Many Linux kernels
	// (notably Ubuntu's generic kernel) don't compile CONFIG_IP_PNP and
	// silently ignore the directive, so we read /proc/cmdline and call
	// ip(8) to bring the interface up. Safe to run unconditionally: if the
	// kernel already configured the interface this is a harmless no-op.
	configureNetworkFromCmdline()

	port := cfg.VsockPort
	if port == 0 {
		port = vsockrpc.DefaultPort
	}
	if err := spawnVsockAgent(port); err != nil {
		logf("WARN: failed to spawn vsock-agent: %v", err)
	}

	chainInit := cfg.ChainInit
	if chainInit == "" {
		chainInit = "/sbin/init"
	}
	if _, err := os.Stat(chainInit); err != nil {
		logf("WARN: chain_init %q not found, falling back to /bin/sh", chainInit)
		chainInit = "/bin/sh"
	}
	logf("exec %s (chain init, PID 1 hand-off)", chainInit)
	if err := syscall.Exec(chainInit, []string{chainInit}, os.Environ()); err != nil {
		logf("FATAL: exec %s failed: %v", chainInit, err)
		os.Exit(1)
	}
}

// spawnVsockAgent re-exec's this binary as a detached child so the vsock
// RPC server survives the PID 1 hand-off. The child inherits no stdio so
// it is fully decoupled from sysbox-init's exec'd successor.
func spawnVsockAgent(port uint32) error {
	self, err := os.Executable()
	if err != nil {
		self = "/sysbox-init"
	}
	cmd := exec.Command(self, "--vsock-agent", strconv.FormatUint(uint64(port), 10))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Redirect stdio to /dev/null so the agent is fully detached.
	dev, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdin = dev
		cmd.Stdout = dev
		cmd.Stderr = dev
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	logf("vsock-agent spawned (pid %d, port %d)", cmd.Process.Pid, port)
	// Don't Wait — we want it detached. The kernel will reparent it to PID 1
	// (the chain_init we're about to exec).
	return nil
}

func mountEssentials() {
	tryMount("proc", "/proc", "proc", 0, "")
	tryMount("sysfs", "/sys", "sysfs", 0, "")
	tryMount("devtmpfs", "/dev", "devtmpfs", 0, "")
}

func tryMount(source, target, fstype string, flags uintptr, data string) {
	if err := os.MkdirAll(target, 0755); err != nil && !os.IsExist(err) {
		logf("mkdir %s: %v", target, err)
		return
	}
	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		// EBUSY means already mounted by the kernel — harmless.
		if !isAlreadyMounted(err) {
			logf("mount %s on %s: %v", source, target, err)
		}
	}
}

func isAlreadyMounted(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok && errno == syscall.EBUSY
}

func mountAndReadConfig() (VMConfig, error) {
	var cfg VMConfig

	if err := os.MkdirAll(configMount, 0755); err != nil {
		return cfg, fmt.Errorf("mkdir %s: %w", configMount, err)
	}
	if err := syscall.Mount(configDevice, configMount, "ext4", syscall.MS_RDONLY, ""); err != nil {
		return cfg, fmt.Errorf("mount %s on %s: %w", configDevice, configMount, err)
	}

	path := filepath.Join(configMount, configFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	logf("loaded config from %s", path)
	return cfg, nil
}

func applyConfig(cfg VMConfig) {
	if cfg.Hostname != "" {
		if err := syscall.Sethostname([]byte(cfg.Hostname)); err != nil {
			logf("sethostname %q: %v", cfg.Hostname, err)
		} else {
			logf("hostname set to %s", cfg.Hostname)
		}
		// Persist into /etc/hostname so systemd (or any other init) does
		// not reset it to whatever the rootfs ships with. Best-effort: a
		// read-only rootfs would silently skip this.
		if err := os.WriteFile("/etc/hostname", []byte(cfg.Hostname+"\n"), 0644); err != nil {
			logf("write /etc/hostname: %v", err)
		}
	}

	if len(cfg.AuthorizedKeys) > 0 {
		if err := installAuthorizedKeys(cfg.AuthorizedKeys); err != nil {
			logf("install authorized_keys: %v", err)
		} else {
			logf("installed %d authorized_keys", len(cfg.AuthorizedKeys))
		}
	}

	if len(cfg.Env) > 0 {
		if err := writeEnvProfile(cfg.Env); err != nil {
			logf("write env profile: %v", err)
		}
	}
}

func installAuthorizedKeys(keys []string) error {
	if err := os.MkdirAll("/root/.ssh", 0700); err != nil {
		return err
	}
	body := strings.Join(keys, "\n") + "\n"
	if err := os.WriteFile("/root/.ssh/authorized_keys", []byte(body), 0600); err != nil {
		return err
	}
	return nil
}

func writeEnvProfile(env map[string]string) error {
	if err := os.MkdirAll("/etc/profile.d", 0755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# generated by sysbox-init\n")
	for k, v := range env {
		fmt.Fprintf(&b, "export %s=%q\n", k, v)
	}
	return os.WriteFile("/etc/profile.d/sysbox-env.sh", []byte(b.String()), 0644)
}

// logf writes a single line to /dev/kmsg if available (so messages show up in
// the Firecracker serial console alongside other kernel messages), with a
// fallback to stderr.
func logf(format string, args ...any) {
	msg := fmt.Sprintf("[sysbox-init] "+format+"\n", args...)
	if f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString(msg)
		_ = f.Close()
		return
	}
	fmt.Fprint(os.Stderr, msg)
}
