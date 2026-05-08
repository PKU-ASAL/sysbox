package commands

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/oslab/sysbox/pkg/sensor"
	"github.com/oslab/sysbox/pkg/session"
	"github.com/oslab/sysbox/pkg/sink"
	"github.com/spf13/cobra"
)

var (
	sensorTraceeBin    string
	sensorDockerMode   bool
	sensorDockerImage  string
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
	Short: "Signal running sensors to stop (SIGTERM their PIDs)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Println("stop: no persistent sensor daemon in Phase 2; sensors run in foreground via 'sensor start'")
		return nil
	},
}

var sensorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sensor status (events written, active sessions)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		stateFile := flagStateFile
		eventsPath := filepath.Join(filepath.Dir(stateFile), "events.jsonl")
		if _, err := os.Stat(eventsPath); os.IsNotExist(err) {
			fmt.Println("No events file found. Run 'sensor start' first.")
			return nil
		}
		fmt.Printf("Events file: %s\n", eventsPath)
		return nil
	},
}

func init() {
	sensorStartCmd.Flags().StringVar(&sensorTraceeBin, "tracee-bin", "", "path to tracee binary (default: use docker mode)")
	sensorStartCmd.Flags().BoolVar(&sensorDockerMode, "docker", true, "run tracee via docker run --privileged (no root required)")
	sensorStartCmd.Flags().StringVar(&sensorDockerImage, "tracee-image", "aquasec/tracee:0.22.0", "tracee Docker image")
	sensorCmd.AddCommand(sensorStartCmd, sensorStopCmd, sensorStatusCmd)
}

func runSensorStart(cmd *cobra.Command, _ []string) error {
	_, _, st, err := loadWorkspace()
	if err != nil {
		return err
	}

	stateFile := flagStateFile
	eventsPath := filepath.Join(filepath.Dir(stateFile), "events.jsonl")
	eventSink := sink.NewJSONLSink(eventsPath)
	defer eventSink.Close()

	lab := session.NewLabeler()

	// Restore any sessions already registered in the cgroup hierarchy.
	for _, r := range st.Resources {
		if r.Type != "sysbox_node" {
			continue
		}
		ids, err := session.NodeCgroupIDs(r.Name)
		if err == nil {
			for sid, cgroupID := range ids {
				lab.RegisterSession(cgroupID, sid)
			}
		}
	}

	// Start a control socket so sshd-hook can register new sessions.
	sockPath := filepath.Join(filepath.Dir(stateFile), "sessions.sock")
	os.Remove(sockPath)
	srv := newControlServer(sockPath, lab)
	go srv.serve()
	defer srv.close()

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var sensors []sensor.Sensor

	for _, r := range st.Resources {
		if r.Type != "sysbox_node" {
			continue
		}
		containerID := asStringFromMap(r.Instance, "container_id")
		if containerID == "" {
			continue
		}

		var backend *sensor.TraceeBackend
		if sensorDockerMode || sensorTraceeBin == "" {
			backend = sensor.NewDockerTraceeBackend(sensorDockerImage, lab)
		} else {
			backend = sensor.NewTraceeBackend(sensorTraceeBin, lab)
		}
		ch, err := backend.Start(ctx, r.Name, containerID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[sensor] node %s: start failed: %v\n", r.Name, err)
			continue
		}
		sensors = append(sensors, backend)

		go func(nodeID string, events <-chan sensor.Event) {
			for e := range events {
				if err := eventSink.Write(e); err != nil {
					fmt.Fprintf(os.Stderr, "[sensor] write error: %v\n", err)
				}
			}
		}(r.Name, ch)

		cid := containerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		fmt.Printf("[sensor] node %s: started (container %s)\n", r.Name, cid)
	}

	if len(sensors) == 0 {
		return fmt.Errorf("no running nodes found in state")
	}

	fmt.Printf("[sensor] writing events to %s\n", eventsPath)
	fmt.Println("[sensor] press Ctrl+C to stop")

	<-ctx.Done()
	for _, s := range sensors {
		_ = s.Stop()
	}
	return nil
}

// controlServer listens on a Unix socket for session register messages
// from sysbox-sshd-hook processes running inside containers.
//
// Message format: {"action":"register","node_id":"..","session_id":".."}
//
// The server uses SO_PEERCRED to get the hook's host PID, creates the
// session cgroup, moves the PID in, and registers the Labeler mapping.
type controlServer struct {
	path string
	lab  *session.Labeler
	l    net.Listener
}

func newControlServer(path string, lab *session.Labeler) *controlServer {
	return &controlServer{path: path, lab: lab}
}

func (s *controlServer) serve() {
	l, err := net.Listen("unix", s.path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sensor] control socket: %v\n", err)
		return
	}
	s.l = l
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

type registerMsg struct {
	Action    string `json:"action"`
	NodeID    string `json:"node_id"`
	SessionID string `json:"session_id"`
}

func (s *controlServer) handleConn(conn net.Conn) {
	defer conn.Close()

	var msg registerMsg
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		return
	}
	if msg.Action != "register" || msg.NodeID == "" || msg.SessionID == "" {
		return
	}

	// Get hook's host PID via SO_PEERCRED so we can move it to the session cgroup.
	ucred, err := peerCred(conn)
	hookPID := 0
	if err == nil {
		hookPID = int(ucred.Pid)
	}

	cgroupID, err := session.CreateSessionCgroup(msg.NodeID, msg.SessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sensor] create cgroup for session %s: %v\n", msg.SessionID, err)
		return
	}

	if hookPID > 0 {
		if err := session.MoveProcess(msg.NodeID, msg.SessionID, hookPID); err != nil {
			fmt.Fprintf(os.Stderr, "[sensor] move pid %d to cgroup: %v\n", hookPID, err)
		}
	}

	s.lab.RegisterSession(cgroupID, msg.SessionID)
	fmt.Printf("[sensor] session registered: node=%s session=%s cgroup_id=%d\n",
		msg.NodeID, msg.SessionID, cgroupID)
}

func (s *controlServer) close() {
	if s.l != nil {
		s.l.Close()
	}
	os.Remove(s.path)
}

func asStringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}
