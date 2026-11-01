// Package guesttask owns durable guest tasks. Both SSH and vsock carry this
// protocol to the same helper; transport disconnection never means task exit.
package guesttask

import (
	"crypto/ed25519"
	"errors"
	"regexp"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/taskprotocol"
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

type Request = taskprotocol.Request
type Record = taskprotocol.Record

type Call struct {
	Control      *substrate.TerminalControl                `json:"control,omitempty"`
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
