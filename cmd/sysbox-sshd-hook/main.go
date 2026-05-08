// sysbox-sshd-hook is a ForceCommand wrapper injected by sysbox_ssh_access.
//
// Flow:
//  1. sshd executes this binary as the ForceCommand before giving the user a shell.
//  2. Hook reads SYSBOX_NODE_ID, SSH_CONNECTION and SYSBOX_REGISTRY_PATH.
//  3. Queries the registry for a pre-declared session_id; falls back to UUID.
//  4. Notifies the sysbox sensor via Unix socket at SYSBOX_CTRL_SOCK, sending:
//       {"action":"register","node_id":"..","session_id":"..","pid":<self_pid>}
//     The sensor (host side) creates the session cgroup and moves the hook's PID
//     (obtained via SO_PEERCRED) into it.
//  5. Waits for the sensor's ACK then exec's the user shell / SSH_ORIGINAL_COMMAND.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/session"
)

func main() {
	nodeID := os.Getenv("SYSBOX_NODE_ID")
	if nodeID == "" {
		// Not in a sysbox container; just exec the shell directly.
		execShell()
		return
	}

	sessionID := resolveSessionID(nodeID)

	ctrlSock := os.Getenv("SYSBOX_CTRL_SOCK")
	if ctrlSock != "" {
		if err := notifySensor(ctrlSock, nodeID, sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "sysbox-sshd-hook: sensor notify failed (continuing): %v\n", err)
		}
	}

	// Set session ID in environment so child processes can read it.
	os.Setenv("SYSBOX_SESSION_ID", sessionID)

	execShell()
}

// resolveSessionID looks up a pre-registered session_id from the registry,
// or generates a fresh UUID.
func resolveSessionID(nodeID string) string {
	regPath := os.Getenv("SYSBOX_REGISTRY_PATH")
	if regPath != "" {
		sourceIP := extractSourceIP(os.Getenv("SSH_CONNECTION"))
		reg := session.NewRegistry(regPath)
		if sid := reg.Resolve(nodeID, sourceIP); sid != "" {
			return sid
		}
	}
	return uuid.New().String()
}

// notifySensor connects to the control socket and sends a register request.
// The sensor host process reads the peer's credential via SO_PEERCRED to get
// the host PID, creates the session cgroup, and moves that PID in.
func notifySensor(sockPath, nodeID, sessionID string) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	msg := map[string]any{
		"action":     "register",
		"node_id":    nodeID,
		"session_id": sessionID,
	}
	return json.NewEncoder(conn).Encode(msg)
}

// execShell replaces the current process with the user shell or the command
// specified by SSH_ORIGINAL_COMMAND.
func execShell() {
	cmd := os.Getenv("SSH_ORIGINAL_COMMAND")
	var argv []string
	if cmd != "" {
		argv = []string{"/bin/sh", "-c", cmd}
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		argv = []string{shell}
	}

	binary, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "sysbox-sshd-hook: cannot find shell %s: %v\n", argv[0], err)
		os.Exit(1)
	}
	if err := syscall.Exec(binary, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "sysbox-sshd-hook: exec failed: %v\n", err)
		os.Exit(1)
	}
}

func extractSourceIP(sshConn string) string {
	// SSH_CONNECTION = "client_ip client_port server_ip server_port"
	parts := strings.Fields(sshConn)
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}
