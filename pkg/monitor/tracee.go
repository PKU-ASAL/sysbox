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
type TraceeBackend struct {
	mu              sync.Mutex
	cancel          context.CancelFunc
	sensorContainer string
}

func (t *TraceeBackend) Name() string { return "tracee" }

// Start activates tracee for all Docker targets.
//
//  1. Resolves each target's mount namespace ID via docker-inspect + /proc.
//  2. Truncates the sink file so the episode starts clean.
//  3. docker exec -d <sensor_container> /tracee/tracee -s mntns=… -o json:… -e …
//  4. Tails the host-side sink file and forwards parsed events to the returned channel.
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

	// Build and launch tracee.
	events := strings.Join(cfg.Events, ",")
	if events == "" {
		events = defaultTraceeEvents
	}
	traceeArgs := []string{
		"-s", "mntns=" + strings.Join(mntnsIDs, ","),
		"-o", "json:" + containerSink,
		"-e", events,
	}
	dockerArgs := append([]string{"exec", "-d", sensorContainer, traceeBin}, traceeArgs...)

	out, err := exec.CommandContext(ctx, "docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("start tracee: %w\n%s", err, out)
	}
	fmt.Printf("[monitor/tracee] started in %s  scope: mntns=%s\n",
		sensorContainer, strings.Join(mntnsIDs, ","))

	tctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.cancel = cancel
	t.sensorContainer = sensorContainer
	t.mu.Unlock()

	// Allow tracee eBPF programs to initialise before we start reading.
	time.Sleep(3 * time.Second)

	ch := make(chan sensor.Event, 1024)
	go t.tailSink(tctx, hostSink, ch)
	return ch, nil
}

// Stop cancels the tail goroutine and kills the tracee process inside the
// sensor container.
func (t *TraceeBackend) Stop(ctx context.Context) error {
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	sc := t.sensorContainer
	if sc == "" {
		sc = defaultSensorContainer
	}
	t.mu.Unlock()

	exec.CommandContext(ctx, "docker", "exec", sc, "pkill", "-9", "tracee").Run() //nolint:errcheck
	return nil
}

// tailSink tails path and sends parsed events to ch until ctx is cancelled.
// Uses bufio.Reader.ReadBytes so it never stalls at EOF (unlike bufio.Scanner).
func (t *TraceeBackend) tailSink(ctx context.Context, path string, ch chan<- sensor.Event) {
	defer close(ch)

	// Wait until tracee writes the first byte.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
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
