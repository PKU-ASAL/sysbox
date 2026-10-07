// Package hostsupervisor owns provider access for signed guest task operations.
// Workers send resource identities, never arbitrary host paths or connections.
package hostsupervisor

import (
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
	Version   int                                `json:"version"`
	Operation string                             `json:"operation"`
	Target    TargetRef                          `json:"target"`
	Task      *guesttask.Request                 `json:"task,omitempty"`
	Handle    substrate.ExecutionHandle          `json:"handle,omitempty"`
	Grant     *controlplane.SignedExecutionGrant `json:"grant,omitempty"`
	Stream    string                             `json:"stream,omitempty"`
	Cursor    int64                              `json:"cursor,omitempty"`
	Limit     int                                `json:"limit,omitempty"`
}
type Response struct {
	Version int                       `json:"version"`
	BootID  string                    `json:"boot_id,omitempty"`
	Handle  substrate.ExecutionHandle `json:"handle,omitempty"`
	Result  substrate.ExecResult      `json:"result,omitempty"`
	Output  substrate.ExecutionOutput `json:"output,omitempty"`
	Error   string                    `json:"error,omitempty"`
}
