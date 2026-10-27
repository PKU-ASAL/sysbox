// Package guesttask owns durable guest tasks. Both SSH and vsock carry this
// protocol to the same helper; transport disconnection never means task exit.
package guesttask

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

const Version = 1

// RecordVersion is independent of the RPC envelope version. Version 1 task
// data requires explicit rebuild; it cannot represent signed stop tombstones.
const RecordVersion = 2
const MaxInput = 8 << 20
const MaxOutput = 8 << 20
const MaxCallSize = 32 << 20
const MaxGrantTTL = 5 * time.Minute

var ErrConflict = errors.New("guest task identity conflict")
var ErrUnauthorized = errors.New("guest task grant rejected")
var ErrUnknown = errors.New("guest task outcome unknown")
var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type SignedGrant = controlplane.SignedExecutionGrant

func verifyGrant(g SignedGrant, key ed25519.PublicKey, boot string, now time.Time) error {
	if err := controlplane.VerifyExecutionGrant(key, g, boot, now); err != nil {
		return ErrUnauthorized
	}
	if g.Grant.Scope.TopologyUID == "" || g.Grant.Scope.ResourceUID == "" || g.Grant.Scope.Generation < 1 || g.Grant.Epoch < 1 || g.Grant.SessionID == "" || g.Grant.AgentID == "" || g.Grant.ActionID == "" || g.Grant.CommandID == "" || g.Grant.Attempt < 1 || g.Grant.Operation != "guest_task" || g.Grant.State != controlplane.GrantActive {
		return ErrUnauthorized
	}
	return nil
}

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
type Call struct {
	PolicyQuery  *network.GuestPolicyRequest               `json:"policy_query,omitempty"`
	SSHQuery     *substrate.SSHAccessQuery                 `json:"ssh_query,omitempty"`
	Cancellation *controlplane.SignedExecutionCancellation `json:"cancellation,omitempty"`
	FileRequest  *substrate.FileInstallRequest             `json:"file_request,omitempty"`
	FileHandle   *substrate.FileInstallHandle              `json:"file_handle,omitempty"`
	FileData     []byte                                    `json:"file_data,omitempty"`
	SSHMutation  *substrate.SSHAccessMutation              `json:"ssh_mutation,omitempty"`
	SSHHandle    *substrate.SSHAccessHandle                `json:"ssh_handle,omitempty"`
	SSHUser      string                                    `json:"ssh_user,omitempty"`
	SSHAddress   string                                    `json:"ssh_address,omitempty"`
	SSHPort      int                                       `json:"ssh_port,omitempty"`
	BootID       string                                    `json:"boot_id,omitempty"`
	Epoch        uint64                                    `json:"epoch,omitempty"`
	Scope        *controlplane.ExecutionScope              `json:"scope,omitempty"`
	Grant        *SignedGrant                              `json:"grant,omitempty"`
	Version      int                                       `json:"version"`
	Operation    string                                    `json:"operation"`
	Request      *Request                                  `json:"request,omitempty"`
	ID           string                                    `json:"id,omitempty"`
	Digest       string                                    `json:"digest,omitempty"`
	Stream       string                                    `json:"stream,omitempty"`
	Cursor       int64                                     `json:"cursor,omitempty"`
	Limit        int                                       `json:"limit,omitempty"`
}
type Response struct {
	Policy         *driver.RulesetObservation         `json:"policy,omitempty"`
	PolicyAbsent   bool                               `json:"policy_absent,omitempty"`
	SSHStop        *substrate.SSHAccessStop           `json:"ssh_stop,omitempty"`
	SSHDiscovery   *substrate.SSHAccessDiscovery      `json:"ssh_discovery,omitempty"`
	FileInspection *substrate.InstalledFileInspection `json:"file_inspection,omitempty"`
	FileHandle     *substrate.FileInstallHandle       `json:"file_handle,omitempty"`
	FileRecord     *FileInstallRecord                 `json:"file_record,omitempty"`
	SSHResult      *substrate.SSHAccessResult         `json:"ssh_result,omitempty"`
	SSHHandle      *substrate.SSHAccessHandle         `json:"ssh_handle,omitempty"`
	SSHInspection  *substrate.SSHAccessInspection     `json:"ssh_inspection,omitempty"`
	SSHKeys        []byte                             `json:"ssh_keys,omitempty"`
	Version        int                                `json:"version"`
	BootID         string                             `json:"boot_id,omitempty"`
	Record         *Record                            `json:"record,omitempty"`
	Records        []Record                           `json:"records,omitempty"`
	Output         []byte                             `json:"output,omitempty"`
	NextCursor     int64                              `json:"next_cursor,omitempty"`
	Error          string                             `json:"error,omitempty"`
}
