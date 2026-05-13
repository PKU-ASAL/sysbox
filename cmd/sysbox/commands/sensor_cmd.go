package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/monitor"
	"github.com/oslab/sysbox/pkg/sensor"
	"github.com/oslab/sysbox/pkg/sink"
	"github.com/oslab/sysbox/pkg/state"
)

var (
	sensorSidecar     bool
	sensorSidecarPath string
)

var sensorCmd = &cobra.Command{
	Use:   "sensor",
	Short: "Manage the sysbox sensor (Tracee-backed eBPF observer)",
}

var sensorStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start sensor for all running nodes in current state",
	RunE:  runSensorStart,
}

var sensorStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Signal running sensors to stop",
	RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Println("Sensors run in foreground; stop via Ctrl+C or kill the sysbox process.")
		return nil
	},
}

var sensorRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Stop running sensor, then start fresh (re-resolves node handles after reprovisioning)",
	Long: `Kills any running 'sysbox sensor start' process and tracee inside the sensor
container, then starts a new sensor session. Use this after reprovisioning a
node so the monitor backend picks up the new container's mount namespace.`,
	RunE: runSensorRestart,
}

var sensorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sensor status",
	RunE: func(cmd *cobra.Command, _ []string) error {
		eventsDir := filepath.Join(filepath.Dir(flagStateFile), "events")
		entries, err := os.ReadDir(eventsDir)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No events directory found. Run 'sensor start' first.")
				return nil
			}
			return err
		}
		fmt.Printf("Events dir: %s\n", eventsDir)
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
				info, _ := e.Info()
				size := int64(0)
				if info != nil {
					size = info.Size()
				}
				fmt.Printf("  %-30s %d bytes\n", e.Name(), size)
			}
		}
		return nil
	},
}

func init() {
	sensorStartCmd.Flags().BoolVar(&sensorSidecar, "sidecar", false, "read events from sidecar container file instead of monitor mode")
	sensorStartCmd.Flags().StringVar(&sensorSidecarPath, "sidecar-path", "/tmp/sysbox-events/events.jsonl", "path to sidecar events file (used with --sidecar)")
	sensorCmd.AddCommand(sensorStartCmd, sensorStopCmd, sensorRestartCmd, sensorStatusCmd)
}

func runSensorStart(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Per-node event files live under runs/default/events/<node_id>.jsonl
	eventsDir := filepath.Join(filepath.Dir(flagStateFile), "events")
	eventSink, err := sink.NewRoutingSink(eventsDir)
	if err != nil {
		return fmt.Errorf("create events sink: %w", err)
	}
	defer eventSink.Close()

	// Look for sysbox_monitor resources in state.
	_, _, st, err := loadWorkspace()
	if err != nil {
		return err
	}

	var monitorResources []state.Resource
	for _, r := range st.Resources {
		if r.Type == "sysbox_monitor" {
			monitorResources = append(monitorResources, r)
		}
	}

	// ── Monitor mode ────────────────────────────────────────────────────────
	// Preferred path: sysbox_monitor declared in HCL → activate registered backends.
	if len(monitorResources) > 0 {
		return runMonitorMode(ctx, monitorResources, st, eventSink)
	}

	// ── Legacy sidecar mode ──────────────────────────────────────────────────
	// Backward compatibility: no sysbox_monitor in state → tail a pre-started
	// tracee sidecar file directly (previous default behaviour).
	if sensorSidecar {
		fmt.Println("[sensor] no sysbox_monitor in state; using legacy sidecar mode")
		return runSidecarTail(ctx, sensorSidecarPath, eventSink)
	}

	return fmt.Errorf("no sysbox_monitor resources in state; run 'sysbox apply' first, or use --sidecar for legacy mode")
}

func runSensorRestart(cmd *cobra.Command, args []string) error {
	fmt.Println("[sensor] stopping previous sensor instance...")
	stopRunningSensor()
	fmt.Println("[sensor] restarting...")
	return runSensorStart(cmd, args)
}

// stopRunningSensor kills any background 'sysbox sensor start' process and
// the tracee binary inside the sensor container. Safe to call when no sensor
// is running (errors are silently ignored).
func stopRunningSensor() {
	// Kill any running 'sysbox sensor start' process.
	// Pattern uses a literal space before "start" so it does NOT match
	// 'sysbox sensor restart' (which contains "re start" as a substring
	// but not the two-word sequence "sensor start").
	exec.Command("pkill", "-f", "sysbox.*sensor start").Run() //nolint:errcheck
	// Kill tracee inside the sensor sidecar container.
	exec.Command("docker", "exec", "sysbox-sensor", "pkill", "-9", "tracee").Run() //nolint:errcheck
	time.Sleep(500 * time.Millisecond)
}

// runMonitorMode activates each sysbox_monitor resource via its registered Backend.
func runMonitorMode(ctx context.Context, monitors []state.Resource, st *state.State, eventSink sink.EventSink) error {
	collector := monitor.NewCollector(eventSink)

	var channels []<-chan sensor.Event
	var activeBackends []monitor.Backend

	for _, m := range monitors {
		backendName := asStringFromMap(m.Instance, "backend")
		if backendName == "" {
			backendName = "tracee"
		}

		b, err := monitor.Get(backendName)
		if err != nil {
			return fmt.Errorf("monitor %s: %w", m.Name, err)
		}

		targets := monitorsTargets(m, st)
		cfg := monitorConfig(m)

		ch, err := b.Start(ctx, targets, cfg)
		if err != nil {
			return fmt.Errorf("monitor %s: start %s: %w", m.Name, backendName, err)
		}

		channels = append(channels, ch)
		activeBackends = append(activeBackends, b)
		fmt.Printf("[sensor] monitor %-12s backend=%-8s nodes=%v\n",
			m.Name, backendName, m.Instance["nodes"])
	}

	fmt.Println("[sensor] press Ctrl+C to stop")
	collector.Run(ctx, channels...)

	stopCtx := context.Background()
	for _, b := range activeBackends {
		b.Stop(stopCtx) //nolint:errcheck
	}
	return nil
}

// monitorsTargets resolves monitor.Targets dynamically from the current node
// state. This ensures we always use live container handles (container_id,
// mntns) even after a node has been reprovisioned with a new container.
func monitorsTargets(m state.Resource, st *state.State) []monitor.Target {
	nodes, _ := m.Instance["nodes"].([]any)
	targets := make([]monitor.Target, 0, len(nodes))
	for _, n := range nodes {
		nodeName, _ := n.(string)
		if nodeName == "" {
			continue
		}
		nodeState := st.FindResource("sysbox_node", nodeName)
		if nodeState == nil {
			fmt.Fprintf(os.Stderr, "[sensor] warn: node %s not in state, skipping\n", nodeName)
			continue
		}
		targets = append(targets, monitor.Target{
			NodeID:    nodeName,
			Substrate: nodeState.Provider,
			Handle: map[string]string{
				"container_id":   asStringFromMap(nodeState.Instance, "container_id"),
				"container_name": fmt.Sprintf("sysbox-%s", nodeName),
			},
		})
	}
	return targets
}

// monitorConfig reconstructs monitor.Config from the state Instance map.
func monitorConfig(m state.Resource) monitor.Config {
	var events []string
	if evs, ok := m.Instance["events"].([]any); ok {
		for _, e := range evs {
			if s, ok := e.(string); ok {
				events = append(events, s)
			}
		}
	}
	extra := map[string]string{}
	if em, ok := m.Instance["extra"].(map[string]any); ok {
		for k, v := range em {
			if s, ok := v.(string); ok {
				extra[k] = s
			}
		}
	}
	return monitor.Config{Events: events, Extra: extra}
}

// runSidecarTail tails the sidecar events file and forwards parsed events to
// the sink. It waits up to 30s for the file to appear (tracee startup takes a
// few seconds), then tails indefinitely until ctx is done.
//
// Events are routed to per-node files via the sink (RoutingSink), so NodeID
// attribution happens automatically from tracee's container.name enrichment.
func runSidecarTail(ctx interface{ Done() <-chan struct{} }, srcPath string, eventSink sink.EventSink) error {
	fmt.Printf("[sensor] sidecar mode: tailing %s\n", srcPath)

	// Wait for the sidecar to create the events file.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(srcPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar events file not found after 30s: %s\nEnsure sysbox_node.sensor is running", srcPath)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
	fmt.Printf("[sensor] sidecar events file found, tailing...\n")

	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open sidecar events file: %w", err)
	}
	defer f.Close()

	// Seek to end so we only forward new events (not replay history).
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek sidecar events file: %w", err)
	}

	// bufio.Reader is used instead of bufio.Scanner: Scanner permanently
	// sets done=true after the first EOF, so it never reads new data appended
	// to the file. Reader.ReadBytes('\n') returns io.EOF at the current end
	// and correctly resumes reading on subsequent calls once data appears.
	reader := bufio.NewReaderSize(f, 1<<20) // 1 MiB read buffer

	fmt.Println("[sensor] press Ctrl+C to stop")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var partial []byte // accumulates incomplete lines across ticks
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					// Prepend any leftover bytes from the previous read.
					if len(partial) > 0 {
						line = append(partial, line...)
						partial = nil
					}
					// Only process complete lines (end with '\n').
					if line[len(line)-1] == '\n' {
						line = line[:len(line)-1] // strip newline
						if len(line) > 0 && line[len(line)-1] == '\r' {
							line = line[:len(line)-1]
						}
						var e sensor.Event
						if perr := sensor.ParseTraceeJSON(line, &e); perr == nil {
							if werr := eventSink.Write(e); werr != nil {
								fmt.Fprintf(os.Stderr, "[sensor] write error: %v\n", werr)
							}
						}
					} else {
						// Incomplete line — save for next tick.
						partial = append(partial, line...)
					}
				}
				if err == io.EOF {
					break // no more data right now; wait for next tick
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "[sensor] read error: %v\n", err)
					break
				}
			}
		}
	}
}

func asStringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}
