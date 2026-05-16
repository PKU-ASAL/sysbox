package monitor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/sensor"
)

const (
	defaultTraceeBin         = "/tracee/tracee"
	defaultSensorContainer   = "sysbox-sensor"
	defaultHostSinkPath      = "/tmp/sysbox-events/events.jsonl"
	defaultContainerSinkPath = "/tmp/events/events.jsonl"
	defaultTraceeEvents      = "execve,execveat,openat,connect,accept4,clone,fork,vfork,sched_process_exit"
	defaultTraceePidFile     = "/run/tracee.pid"
	traceeLogFile           = "/tmp/tracee-stdout.log" // host-side log for tracee stdout/stderr
)

// TraceeBackend implements Backend using the tracee eBPF sensor.
//
// Architecture:
//   - Tracee runs inside a privileged "sensor" sidecar container that has
//     access to /proc, /sys/kernel/btf/vmlinux, /sys/fs/bpf, and the Docker socket.
//   - Scope filtering uses mount namespace IDs (-s mntns=...) rather than
//     tracee's cgroup-based container filter, which fails for sysbox runtime
//     containers that sit outside Docker's standard cgroup hierarchy.
//   - Tracee enriches each event with container.name via the Docker socket;
//     ParseTraceeJSON strips the "sysbox-" prefix to produce Event.NodeID.
//
// Config.Extra keys (all optional):
//
//	sensor_container    – Docker container hosting tracee (default: sysbox-sensor)
//	tracee_bin          – path to binary inside that container (default: /tracee/tracee)
//	host_sink_path      – host-side path for the events file (default: /tmp/sysbox-events/events.jsonl)
//	container_sink_path – in-container path for the same file (default: /tmp/events/events.jsonl)
//
// Startup detection:
//   - Tracee is launched as a foreground docker exec inside a goroutine so
//     errors surface to errCh rather than being silently swallowed by detached mode.
//   - The Ready() method blocks until tracee has written its first event byte
//     (delegated to tailSink's existing poll loop), or returns an error if
//     tracee exits before producing output.
//   - ErrCh() returns a channel that receives an error if tracee exits
//     unexpectedly after startup.
//
// Graceful shutdown:
//   - Stop sends SIGTERM to tracee (using the PID file written at startup),
//     waits up to 5 seconds, then escalates to SIGKILL if needed.
//   - Falls back to pkill if the kill command is unavailable in the container.
type TraceeBackend struct {
	mu              sync.Mutex
	cancel          context.CancelFunc
	sensorContainer string
	cmdCancel       context.CancelFunc // cancels the foreground docker exec
	errCh           chan error         // receives error if tracee exits unexpectedly
	readyCh         chan struct{}      // closed once tailSink sees first byte
}

func (t *TraceeBackend) Name() string { return "tracee" }

// Start activates tracee for all Docker targets.
//
//  1. Resolves each target's mount namespace ID via docker-inspect + /proc.
//  2. Truncates the sink file so the episode starts clean.
//  3. Launches tracee as a foreground docker exec in a goroutine (capturing
//     stdout/stderr to a host-side log file) so startup errors are detectable.
//  4. Tails the host-side sink file and forwards parsed events to the returned channel.
//     tailSink's existing poll loop waits for the first byte, replacing the
//     old fixed sleep.
func (t *TraceeBackend) Start(ctx context.Context, targets []Target, cfg Config) (<-chan sensor.Event, error) {
	extra := cfg.Extra
	if extra == nil {
		extra = map[string]string{}
	}
	sensorContainer := orDefault(extra["sensor_container"], defaultSensorContainer)
	traceeBin := orDefault(extra["tracee_bin"], defaultTraceeBin)
	hostSink := orDefault(extra["host_sink_path"], defaultHostSinkPath)
	containerSink := orDefault(extra["container_sink_path"], defaultContainerSinkPath)

	// Collect mntns IDs for all Docker targets.
	var mntnsIDs []string
	for _, tgt := range targets {
		if tgt.Substrate != "docker" {
			fmt.Fprintf(os.Stderr, "[monitor/tracee] skip non-docker target %s (substrate=%s)\n",
				tgt.NodeID, tgt.Substrate)
			continue
		}
		name := tgt.Handle["container_name"]
		if name == "" {
			name = "sysbox-" + tgt.NodeID
		}
		id, err := dockerMntNS(name)
		if err != nil {
			return nil, fmt.Errorf("mntns for %s: %w", tgt.NodeID, err)
		}
		mntnsIDs = append(mntnsIDs, id)
		fmt.Printf("[monitor/tracee] %-20s mntns=%s\n", tgt.NodeID, id)
	}
	if len(mntnsIDs) == 0 {
		return nil, fmt.Errorf("no valid docker targets to monitor")
	}

	// Truncate sink so each session starts clean.
	if err := os.MkdirAll(filepath.Dir(hostSink), 0755); err != nil {
		return nil, fmt.Errorf("create sink dir: %w", err)
	}
	if f, err := os.OpenFile(hostSink, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644); err != nil {
		return nil, fmt.Errorf("truncate sink: %w", err)
	} else {
		f.Close()
	}

	// Build and launch tracee as a foreground process inside a goroutine.
	events := strings.Join(cfg.Events, ",")
	if events == "" {
		events = defaultTraceeEvents
	}
	traceeArgs := []string{
		"--pid-file", defaultTraceePidFile,
		"-s", "mntns=" + strings.Join(mntnsIDs, ","),
		"-o", "json:" + containerSink,
		"-e", events,
	}
	dockerArgs := append([]string{"exec", sensorContainer, traceeBin}, traceeArgs...)

	// Open a host-side log file for tracee stdout/stderr so errors are captured.
	logF, err := os.Create(traceeLogFile)
	if err != nil {
		return nil, fmt.Errorf("create tracee log %s: %w", traceeLogFile, err)
	}

	cmdCtx, cmdCancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cmdCtx, "docker", dockerArgs...)
	cmd.Stdout = logF
	cmd.Stderr = logF

	if err := cmd.Start(); err != nil {
		logF.Close()
		cmdCancel()
		return nil, fmt.Errorf("start tracee: %w", err)
	}
	fmt.Printf("[monitor/tracee] started in %s  scope: mntns=%s  pid-file=%s\n",
		sensorContainer, strings.Join(mntnsIDs, ","), defaultTraceePidFile)

	errCh := make(chan error, 1)
	readyCh := make(chan struct{})

	// Supervise tracee in the background; forward any exit error to errCh.
	go func() {
		defer logF.Close()
		if err := cmd.Wait(); err != nil && cmdCtx.Err() == nil {
			// Tracee exited unexpectedly (not cancelled by us).
			errCh <- fmt.Errorf("tracee exited: %w", err)
		}
	}()

	tctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.cancel = cancel
	t.cmdCancel = cmdCancel
	t.sensorContainer = sensorContainer
	t.errCh = errCh
	t.readyCh = readyCh
	t.mu.Unlock()

	// No time.Sleep — tailSink's poll loop waits for the first byte,
	// which is a reliable indicator that tracee eBPF programs are live.
	ch := make(chan sensor.Event, 1024)
	go t.tailSink(tctx, hostSink, ch, readyCh)
	return ch, nil
}

// Ready blocks until tracee has written its first event byte (signalled via
// readyCh from tailSink) or until an error occurs (tracee exited, context
// cancelled). Returns nil on success or the first error encountered.
func (t *TraceeBackend) Ready(ctx context.Context) error {
	t.mu.Lock()
	readyCh := t.readyCh
	errCh := t.errCh
	t.mu.Unlock()

	if readyCh == nil {
		return fmt.Errorf("tracee: Ready called before Start")
	}
	select {
	case <-readyCh:
		return nil
	case err := <-errCh:
		return fmt.Errorf("tracee startup: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrCh returns a channel that receives an error if tracee exits unexpectedly
// after startup. Callers can select on this to detect runtime failures.
// The channel is nil before Start is called.
func (t *TraceeBackend) ErrCh() <-chan error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.errCh
}

// Stop cancels the tail goroutine and gracefully stops the tracee process
// inside the sensor container. It sends SIGTERM first, waits up to 5 seconds,
// then escalates to SIGKILL. Falls back to pkill if kill is unavailable.
func (t *TraceeBackend) Stop(ctx context.Context) error {
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	cmdCancel := t.cmdCancel
	sc := t.sensorContainer
	if sc == "" {
		sc = defaultSensorContainer
	}
	t.mu.Unlock()

	// Cancel the foreground docker exec (sends SIGKILL to the docker client,
	// not to tracee inside the container). We still need to kill tracee inside.
	if cmdCancel != nil {
		cmdCancel()
	}

	// --- Graceful shutdown: SIGTERM via PID file, then SIGKILL fallback ---
	// Step 1: Try kill -TERM $(cat /run/tracee.pid)
	pidVal, pidErr := t.containerOutput(ctx, sc, "cat", defaultTraceePidFile)
	if pidErr == nil && pidVal != "" {
		sigTermErr := t.containerRun(ctx, sc, "kill", "-TERM", strings.TrimSpace(pidVal))
		if sigTermErr == nil {
			// Wait up to 5 seconds for tracee to exit.
			if t.waitProcessGone(ctx, sc, strings.TrimSpace(pidVal), 5*time.Second) {
				fmt.Printf("[monitor/tracee] stopped gracefully (SIGTERM)\n")
				return nil
			}
			fmt.Fprintf(os.Stderr, "[monitor/tracee] tracee did not exit after SIGTERM, escalating to SIGKILL\n")
		} else {
			fmt.Fprintf(os.Stderr, "[monitor/tracee] kill -TERM failed (%v), trying fallback\n", sigTermErr)
		}
		// Step 2: SIGKILL via PID file.
		sigKillErr := t.containerRun(ctx, sc, "kill", "-9", strings.TrimSpace(pidVal))
		if sigKillErr == nil {
			fmt.Printf("[monitor/tracee] stopped with SIGKILL (via pid file)\n")
			return nil
		}
		fmt.Fprintf(os.Stderr, "[monitor/tracee] kill -9 via pid failed (%v), trying pkill fallback\n", sigKillErr)
	} else {
		fmt.Fprintf(os.Stderr, "[monitor/tracee] could not read pid file (%v), trying pkill fallback\n", pidErr)
	}

	// Step 3: Fallback — pkill -TERM tracee, then pkill -9 tracee.
	if err := t.containerRun(ctx, sc, "pkill", "-TERM", "tracee"); err == nil {
		if t.waitProcessGone(ctx, sc, "tracee", 5*time.Second) {
			fmt.Printf("[monitor/tracee] stopped gracefully (pkill -TERM)\n")
			return nil
		}
	}
	if err := t.containerRun(ctx, sc, "pkill", "-9", "tracee"); err != nil {
		return fmt.Errorf("stop tracee: all kill methods failed (last pkill -9: %w)", err)
	}
	fmt.Printf("[monitor/tracee] stopped with pkill -9\n")
	return nil
}

// containerRun executes a command inside the sensor container and returns an
// error if the command fails.
func (t *TraceeBackend) containerRun(ctx context.Context, container string, args ...string) error {
	dockerArgs := append([]string{"exec", container}, args...)
	out, err := exec.CommandContext(ctx, "docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker exec %s %s: %w (%s", container, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// containerOutput executes a command inside the sensor container and returns
// its stdout output.
func (t *TraceeBackend) containerOutput(ctx context.Context, container string, args ...string) (string, error) {
	dockerArgs := append([]string{"exec", container}, args...)
	out, err := exec.CommandContext(ctx, "docker", dockerArgs...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// waitProcessGone polls inside the container until the process (identified by
// pid or name) is no longer running, or the timeout expires. Returns true if
// the process exited within the timeout.
func (t *TraceeBackend) waitProcessGone(ctx context.Context, container, identifier string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if the process is still alive using kill -0 (signal 0 = existence check).
		err := t.containerRun(ctx, container, "kill", "-0", identifier)
		if err != nil {
			// Process no longer exists.
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// tailSink tails path and sends parsed events to ch until ctx is cancelled.
// Uses bufio.Reader.ReadBytes so it never stalls at EOF (unlike bufio.Scanner).
// readyCh is closed once the first byte is detected, signalling tracee readiness.
func (t *TraceeBackend) tailSink(ctx context.Context, path string, ch chan<- sensor.Event, readyCh chan struct{}) {
	defer close(ch)

	// Wait until tracee writes the first byte.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			// Tracee has produced output — signal readiness.
			select {
			case <-readyCh:
				// Already closed.
			default:
				close(readyCh)
			}
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "[monitor/tracee] timeout: no events at %s\n", path)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[monitor/tracee] open %s: %v\n", path, err)
		return
	}
	defer f.Close()
	f.Seek(0, io.SeekEnd) //nolint:errcheck

	reader := bufio.NewReaderSize(f, 1<<20) // 1 MiB buffer
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	var partial []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					if len(partial) > 0 {
						line = append(partial, line...)
						partial = nil
					}
					if line[len(line)-1] == '\n' {
						line = line[:len(line)-1]
						if len(line) > 0 && line[len(line)-1] == '\r' {
							line = line[:len(line)-1]
						}
						var ev sensor.Event
						if perr := sensor.ParseTraceeJSON(line, &ev); perr == nil {
							select {
							case ch <- ev:
							case <-ctx.Done():
								return
							}
						}
					} else {
						partial = append(partial, line...)
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "[monitor/tracee] read: %v\n", err)
					break
				}
			}
		}
	}
}

// dockerMntNS returns the numeric mount namespace ID for the named container.
// It runs docker-inspect to get the init PID then reads /proc/<pid>/ns/mnt.
// This works for sysbox runtime containers even though their cgroup hierarchy
// is non-standard — mount namespaces are always reachable via /proc.
func dockerMntNS(containerName string) (string, error) {
	out, err := exec.Command("docker", "inspect", containerName, "--format", "{{.State.Pid}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w", containerName, err)
	}
	pid := strings.TrimSpace(string(out))
	if pid == "0" || pid == "" {
		return "", fmt.Errorf("container %s is not running", containerName)
	}
	link, err := os.Readlink(fmt.Sprintf("/proc/%s/ns/mnt", pid))
	if err != nil {
		return "", fmt.Errorf("readlink mnt ns for pid %s: %w", pid, err)
	}
	// link format: "mnt:[4026533788]"
	link = strings.TrimPrefix(link, "mnt:[")
	link = strings.TrimSuffix(link, "]")
	return link, nil
}

func orDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

func init() {
	Register(&TraceeBackend{})
}
