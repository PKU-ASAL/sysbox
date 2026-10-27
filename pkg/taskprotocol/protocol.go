// Package taskprotocol defines the OS-independent signed guest task wire data.
package taskprotocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"regexp"
)

const MaxInput = 8 << 20
const MaxOutput = 8 << 20
const RecordVersion = 2

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type SignedGrant = controlplane.SignedExecutionGrant
type Request struct {
	UserID      uint32            `json:"user_id"`
	ID          string            `json:"id"`
	Args        []string          `json:"args"`
	Environment map[string]string `json:"environment,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Input       []byte            `json:"input,omitempty"`
	OutputLimit int               `json:"output_limit"`
	Grant       SignedGrant       `json:"grant"`
}

func (r Request) Digest() (string, error) {
	r.Grant = SignedGrant{}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (r Request) Validate() error {
	if r.UserID == 0 || !validID.MatchString(r.ID) || len(r.Args) == 0 || r.Args[0] == "" || len(r.Input) > MaxInput || r.OutputLimit < 1 || r.OutputLimit > MaxOutput {
		return fmt.Errorf("invalid guest task request")
	}
	return nil
}

type Record struct {
	Cancellation         *controlplane.SignedExecutionCancellation `json:"cancellation,omitempty"`
	DeadlineBootNS       int64                                     `json:"deadline_boot_ns"`
	SupervisorPID        int                                       `json:"supervisor_pid"`
	SupervisorStartTicks uint64                                    `json:"supervisor_start_ticks"`
	Version              int                                       `json:"version"`
	ID                   string                                    `json:"id"`
	Digest               string                                    `json:"digest"`
	Grant                SignedGrant                               `json:"grant"`
	BootID               string                                    `json:"boot_id"`
	PID                  int                                       `json:"pid"`
	StartTicks           uint64                                    `json:"start_ticks"`
	CgroupInode          uint64                                    `json:"cgroup_inode"`
	Status               string                                    `json:"status"`
	ExitCode             int                                       `json:"exit_code"`
	ExitCodeKnown        bool                                      `json:"exit_code_known"`
	TerminationConfirmed bool                                      `json:"termination_confirmed"`
	CancelRequested      bool                                      `json:"cancel_requested"`
	StdoutBytes          int64                                     `json:"stdout_bytes"`
	StderrBytes          int64                                     `json:"stderr_bytes"`
	StdoutTruncated      bool                                      `json:"stdout_truncated"`
	StderrTruncated      bool                                      `json:"stderr_truncated"`
	Error                string                                    `json:"error,omitempty"`
}
