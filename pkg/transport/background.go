package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

type executionIdentity struct {
	nodeID     string
	generation int64
}

// BindExecutionIdentity binds results to the resource selected by Node.Connection.
// Call before sharing the connection; identity is immutable while executing.
func (i *executionIdentity) BindExecutionIdentity(nodeID string, generation int64) {
	i.nodeID, i.generation = nodeID, generation
}

func startPOSIX(ctx context.Context, conn substrate.Connection, identity executionIdentity, endpoint string, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	command, err := RemoteCommand(req)
	if err != nil {
		return substrate.ExecutionHandle{}, err
	}
	if req.Stdin != nil {
		return substrate.ExecutionHandle{}, fmt.Errorf("background stdin: %w", substrate.ErrNotSupported)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return substrate.ExecutionHandle{}, err
	}
	nodeID := identity.nodeID
	if nodeID == "" {
		nodeID = endpoint
	}
	handle := substrate.ExecutionHandle{ID: hex.EncodeToString(token[:]), NodeID: nodeID, Generation: identity.generation}
	// One explicitly quoted POSIX wrapper preserves env/cwd/argv as a unit.
	// stdin is detached as well as stdout/stderr so the child survives disconnect.
	result, err := conn.Exec(ctx, substrate.ExecRequest{
		Program: "nohup sh -c " + util.ShellQuote(command) + " </dev/null >/dev/null 2>&1 & echo $!",
		Shell:   substrate.ShellLinux, OutputLimitBytes: 128, Timeout: req.Timeout,
	}, nil, nil)
	if err != nil {
		return handle, err
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 || result.StdoutTruncated {
		return handle, fmt.Errorf("background launch did not return a complete successful result")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil || pid <= 0 {
		return handle, fmt.Errorf("background launch returned an invalid PID")
	}
	handle.PID = pid
	return handle, nil
}
