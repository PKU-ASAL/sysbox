package main

// vm-sensor: an event streamer that runs inside the microVM.
//
// Activation:
//   /sysbox-init --vm-sensor --events execve,fork,exit
//
// The mode is invoked from the host (sysbox sensor) over vsock-rpc OpExec,
// so the host receives one JSON line per event on stdout.
//
// Two backends, tried in order:
//
//  1. tracefs: mount /sys/kernel/tracing, enable sched_process_exec/fork/exit
//     tracepoints, read trace_pipe. Requires CONFIG_FTRACE=y (or =m + modprobe).
//     Zero overhead when idle, exact event timing.
//
//  2. /proc polling: scan /proc every N ms, diff PIDs against the previous
//     snapshot, emit exec/exit events. Works on any Linux kernel, no special
//     config required. Higher latency (~poll interval) but universally available.
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
var tracepoints = map[string]string{
	"execve": "sched/sched_process_exec",
	"fork":   "sched/sched_process_fork",
	"exit":   "sched/sched_process_exit",
}

// runVMSensor is the entry point for --vm-sensor mode. It tries the
// tracefs backend first; if the kernel lacks ftrace it falls back to
// /proc polling. Streaming runs until SIGTERM/SIGINT.
func runVMSensor(eventsCSV, nodeName string) {
	containerName := ""
	if nodeName != "" {
		containerName = "sysbox-" + nodeName
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	// Try tracefs first.
	if tryTracefsBackend(eventsCSV, containerName, sig) {
		return
	}

	// Fallback: /proc polling.
	fmt.Fprintf(os.Stderr, "[vm-sensor] falling back to /proc polling\n")
	runProcPolling(containerName, sig)
}

// ── tracefs backend ──────────────────────────────────────────────────────────

func tryTracefsBackend(eventsCSV, containerName string, sig chan os.Signal) bool {
	if err := mountTracefs(); err != nil {
		fmt.Fprintf(os.Stderr, "[vm-sensor] tracefs unavailable: %v\n", err)
		return false
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
		fmt.Fprintf(os.Stderr, "[vm-sensor] no events enabled via tracefs\n")
		return false
	}

	tracePipe, err := os.Open(filepath.Join(tracefsRoot, "trace_pipe"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[vm-sensor] open trace_pipe: %v\n", err)
		return false
	}
	defer tracePipe.Close()

	// Cleanup on signal so we don't leave tracing enabled.
	go func() {
		<-sig
		for _, p := range enabled {
			_ = writeTraceFile(filepath.Join(tracefsRoot, "events", p, "enable"), "0")
		}
		os.Exit(0)
	}()

	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(tracePipe)
	scanner.Buffer(make([]byte, 256*1024), 1<<20)
	for scanner.Scan() {
		ev := parseTraceLine(scanner.Bytes(), containerName)
		if ev == nil {
			continue
		}
		_ = enc.Encode(ev)
	}
	return true
}

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

// trace_pipe line format:
//
//	            sh-145     [000] ...1.    12.345678: sched_process_exec: filename=/bin/ls pid=145 old_pid=145
//	  <idle>-0   [000] d.s.    12.346000: sched_process_fork: comm=systemd pid=1 child_comm=sh child_pid=145
var traceLineRE = regexp.MustCompile(`^\s*(\S+?)-(\d+)\s+\[\S*\]\s+\S+\s+([0-9.]+):\s+([a-zA-Z_]+):\s*(.*)$`)
var fieldRE = regexp.MustCompile(`(\w+)=(\S+)`)

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

// ── /proc polling backend ────────────────────────────────────────────────────
//
// Periodically scans /proc for new/removed PIDs and emits exec/exit events.
// Works on any Linux kernel without special config. Poll interval is 500ms.

const procPollInterval = 500 * time.Millisecond

// procEntry holds per-PID metadata read from /proc/<pid>/{comm,stat}.
type procEntry struct {
	pid  int
	ppid int
	comm string
	args string // first N bytes of cmdline (truncated at null)
}

func runProcPolling(containerName string, sig chan os.Signal) {
	enc := json.NewEncoder(os.Stdout)
	prev := scanProc()

	// Emit the initial process inventory as execve events so the host
	// sees what's already running, not just processes born after the
	// sensor started. Without this, idle VMs produce zero events.
	for _, e := range prev {
		_ = enc.Encode(procEventToJSON("execve", e, containerName))
	}

	ticker := time.NewTicker(procPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sig:
			os.Exit(0)
		case <-ticker.C:
			cur := scanProc()

			// New PIDs → exec event.
			for pid, e := range cur {
				if _, existed := prev[pid]; !existed {
					_ = enc.Encode(procEventToJSON("execve", e, containerName))
				}
			}
			// Gone PIDs → exit event.
			for pid, e := range prev {
				if _, still := cur[pid]; !still {
					_ = enc.Encode(procEventToJSON("exit", e, containerName))
				}
			}
			prev = cur
		}
	}
}

func procEventToJSON(eventName string, e procEntry, containerName string) map[string]any {
	ev := map[string]any{
		"eventName":           eventName,
		"timestamp":           time.Now().UnixNano(),
		"hostProcessId":       e.pid,
		"hostParentProcessId": e.ppid,
		"comm":                e.comm,
	}
	if e.args != "" {
		ev["filename"] = e.args
	}
	if containerName != "" {
		ev["container"] = map[string]any{"name": containerName}
	}
	return ev
}

// scanProc reads /proc and returns a map of live PIDs with their metadata.
// Silently skips entries that vanish between readdir and read (race-safe).
func scanProc() map[int]procEntry {
	entries := map[int]procEntry{}

	d, err := os.Open("/proc")
	if err != nil {
		return entries
	}
	defer d.Close()

	names, err := d.Readdirnames(-1)
	if err != nil {
		return entries
	}

	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue // not a PID directory
		}

		comm := readProcString(fmt.Sprintf("/proc/%d/comm", pid))
		ppid := readProcPPID(fmt.Sprintf("/proc/%d/stat", pid))
		args := readProcCmdline(fmt.Sprintf("/proc/%d/cmdline", pid))

		entries[pid] = procEntry{
			pid:  pid,
			ppid: ppid,
			comm: strings.TrimSpace(comm),
			args: args,
		}
	}
	return entries
}

func readProcString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// readProcPPID extracts the ppid field from /proc/<pid>/stat.
// Format: pid (comm) state ppid ...
// The comm field may contain spaces or parens, so we find the *last* ')'
// and count fields after it.
func readProcPPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := string(b)
	// Find the last ')' to handle comm with spaces/parens.
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(s[idx+1:])
	// fields[0] = state, fields[1] = ppid
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// readProcCmdline reads /proc/<pid>/cmdline and returns a space-joined
// representation (null bytes replaced with spaces). Truncated to 256 bytes.
func readProcCmdline(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	// Replace null bytes with spaces, trim trailing nulls.
	s := strings.TrimRight(string(b), "\x00")
	s = strings.ReplaceAll(s, "\x00", " ")
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}
