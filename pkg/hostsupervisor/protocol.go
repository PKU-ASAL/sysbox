// Package hostsupervisor owns provider access for signed guest task operations.
// Workers send resource identities, never arbitrary host paths or connections.
package hostsupervisor

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

type TargetRef struct {
	Topology string                      `json:"topology"`
	Node     string                      `json:"node"`
	Scope    controlplane.ExecutionScope `json:"scope"`
}
type Request struct {
	SSHAccessID     string                             `json:"ssh_access_id,omitempty"`
	Version         int                                `json:"version"`
	Operation       string                             `json:"operation"`
	Target          TargetRef                          `json:"target"`
	Task            *guesttask.Request                 `json:"task,omitempty"`
	Handle          substrate.ExecutionHandle          `json:"handle,omitempty"`
	Grant           *controlplane.SignedExecutionGrant `json:"grant,omitempty"`
	Power           *controlplane.NodePowerRequest     `json:"power,omitempty"`
	FileRequest     *substrate.FileInstallRequest      `json:"file_request,omitempty"`
	FileHandle      substrate.FileInstallHandle        `json:"file_handle,omitempty"`
	RunRequest      *RunRequest                        `json:"run_request,omitempty"`
	RecoveryRequest *RunRecoveryRequest                `json:"recovery_request,omitempty"`
	SSHRequest      *controlplane.SSHAccessRequest     `json:"ssh_request,omitempty"`
	AdoptionRequest *controlplane.AdoptionRequest      `json:"adoption_request,omitempty"`
	GCCandidate     *GCCandidate                       `json:"gc_candidate,omitempty"`
	Stream          string                             `json:"stream,omitempty"`
	Cursor          int64                              `json:"cursor,omitempty"`
	Limit           int                                `json:"limit,omitempty"`
}
type Response struct {
	SSHDiscovery     *substrate.SSHAccessDiscovery      `json:"ssh_discovery,omitempty"`
	FileInspection   *substrate.InstalledFileInspection `json:"file_inspection,omitempty"`
	InspectedFile    *substrate.FileInstallHandle       `json:"inspected_file,omitempty"`
	Version          int                                `json:"version"`
	BootID           string                             `json:"boot_id,omitempty"`
	Handle           substrate.ExecutionHandle          `json:"handle,omitempty"`
	Result           substrate.ExecResult               `json:"result,omitempty"`
	Output           substrate.ExecutionOutput          `json:"output,omitempty"`
	Error            string                             `json:"error,omitempty"`
	FileResult       substrate.FileInstallResult        `json:"file_result,omitempty"`
	RunObservation   *RunObservation                    `json:"run_observation,omitempty"`
	RecoveryReport   *controlplane.RunRecoveryReport    `json:"recovery_report,omitempty"`
	SSHEvidence      json.RawMessage                    `json:"ssh_evidence,omitempty"`
	AdoptionEvidence json.RawMessage                    `json:"adoption_evidence,omitempty"`
	GCResult         json.RawMessage                    `json:"gc_result,omitempty"`
}
