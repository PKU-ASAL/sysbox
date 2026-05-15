package main

// vm-sensor: a tracefs-based event streamer that runs inside the microVM.
//
// Activation:
//   /sysbox-init --vm-sensor --events execve,fork,exit
//
// The mode is invoked from the host (sysbox sensor) over vsock-rpc OpExec,
// so the host receives one JSON line per event on stdout. No eBPF / BTF
// dependencies — uses /sys/kernel/tracing tracepoints, which work on any
// kernel ≥ 4.1 that has CONFIG_TRACING=y.
//
// Output schema is intentionally tracee-flavoured so the host-side
// sensor.ParseTraceeJSON can ingest it without a parallel parser:
//
//   {"eventName":"execve","timestamp":<unix_ns>,
//    "hostProcessId":<pid>,"hostParentProcessId":<ppid>,
//    "container":{"name":"sysbox-<node>"},
//    "filename":"<path>","comm":"<comm>"}

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const tracefsRoot = "/sys/kernel/tracing"

// tracepoints map our CSV labels to kernel tracepoint paths under tracefs.
// Keep the set small: every enabled tracepoint adds overhead to the guest.
var tracepoints = map[string]string{
	"execve": "sched/sched_process_exec",
	"fork":   "sched/sched_process_fork",
	"exit":   "sched/sched_process_exit",
}

// runVMSensor is the entry point for --vm-sensor mode. It mounts tracefs,
// enables the requested tracepoints, reads trace_pipe, and prints one
// JSON event per line to stdout. Returns only on fatal setup error;
// streaming runs until SIGTERM/SIGINT.
func runVMSensor(eventsCSV, nodeName string) {
	if err := mountTracefs(); err != nil {
		fmt.Fprintf(os.Stderr, "[vm-sensor] mount tracefs: %v\n", err)
		os.Exit(1)
	}

	var enabled []string
	for _, e := range strings.Split(eventsCSV, ",") {
		e = strings.TrimSpace(e)
		path, ok := tracepoints[e]
		if !ok {
			fmt.Fprintf(os.Stderr, "[vm-sensor] unknown event %q (known: execve,fork,exit)\n", e)
			continue
		}
		if err := writeTraceFile(filepath.Join(tracefsRoot, "events", path, "enable"), "1"); err != nil {
			fmt.Fprintf(os.Stderr, "[vm-sensor] enable %s: %v\n", path, err)
			continue
		}
		enabled = append(enabled, path)
	}
	if len(enabled) == 0 {
		fmt.Fprintf(os.Stderr, "[vm-sensor] no events enabled; aborting\n")
		os.Exit(2)
	}

	// Disable + cleanup on signal so we don't leave tracing enabled.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		for _, p := range enabled {
			_ = writeTraceFile(filepath.Join(tracefsRoot, "events", p, "enable"), "0")
		}
		os.Exit(0)
	}()

	tracePipe, err := os.Open(filepath.Join(tracefsRoot, "trace_pipe"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[vm-sensor] open trace_pipe: %v\n", err)
		os.Exit(3)
	}
	defer tracePipe.Close()

	containerName := ""
	if nodeName != "" {
		containerName = "sysbox-" + nodeName
	}

	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(tracePipe)
	// trace_pipe can produce long lines (especially exec with argv); raise
	// the scanner buffer well above the default 64 KiB.
	scanner.Buffer(make([]byte, 256*1024), 1<<20)
	for scanner.Scan() {
		ev := parseTraceLine(scanner.Bytes(), containerName)
		if ev == nil {
			continue
		}
		_ = enc.Encode(ev)
	}
}

// mountTracefs ensures /sys/kernel/tracing is a usable tracefs mount.
// systemd usually mounts it lazily via the kernel-tracing.mount unit, but
// our minimal Ubuntu rootfs skips that on the ConditionPathExists check.
func mountTracefs() error {
	if _, err := os.Stat(filepath.Join(tracefsRoot, "trace_pipe")); err == nil {
		return nil
	}
	if err := os.MkdirAll(tracefsRoot, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", tracefsRoot, err)
	}
	if err := syscall.Mount("tracefs", tracefsRoot, "tracefs", 0, ""); err != nil {
		return fmt.Errorf("mount tracefs: %w", err)
	}
	return nil
}

func writeTraceFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// trace_pipe line format (printf-style, kernel-version-dependent):
//
//   "            sh-145     [000] ...1.    12.345678: sched_process_exec: filename=/bin/ls pid=145 old_pid=145"
//   "<idle>-0   [000] d.s.    12.346000: sched_process_fork: comm=systemd pid=1 child_comm=sh child_pid=145"
//
// The leading column is right-aligned comm-pid; we tolerate any whitespace.
var traceLineRE = regexp.MustCompile(`^\s*(\S+?)-(\d+)\s+\[\S*\]\s+\S+\s+([0-9.]+):\s+([a-zA-Z_]+):\s*(.*)$`)

// fieldRE pulls "key=value" pairs out of the body. Values run to the next
// space or end of line. Quoted strings are not used by the kernel format.
var fieldRE = regexp.MustCompile(`(\w+)=(\S+)`)

// parseTraceLine converts one raw tracefs line into a tracee-flavoured
// event map suitable for ParseTraceeJSON on the host side.
func parseTraceLine(line []byte, containerName string) map[string]any {
	m := traceLineRE.FindSubmatch(line)
	if m == nil {
		return nil
	}
	comm := string(m[1])
	pid, _ := strconv.Atoi(string(m[2]))
	tracepoint := string(m[4])
	body := string(m[5])

	ev := map[string]any{
		"timestamp":     time.Now().UnixNano(),
		"hostProcessId": pid,
		"comm":          comm,
	}
	if containerName != "" {
		ev["container"] = map[string]any{"name": containerName}
	}

	for _, kv := range fieldRE.FindAllStringSubmatch(body, -1) {
		k, v := kv[1], kv[2]
		switch k {
		case "filename":
			ev["filename"] = v
		case "child_pid":
			if cp, err := strconv.Atoi(v); err == nil {
				ev["childProcessId"] = cp
			}
		case "child_comm":
			ev["childComm"] = v
		case "old_pid", "pid":
			// already captured from the leading column
		default:
			ev[k] = v
		}
	}

	// Map tracepoint name to tracee-style eventName for category routing.
	switch tracepoint {
	case "sched_process_exec":
		ev["eventName"] = "execve"
	case "sched_process_fork":
		ev["eventName"] = "fork"
	case "sched_process_exit":
		ev["eventName"] = "exit"
	default:
		ev["eventName"] = tracepoint
	}
	return ev
}
