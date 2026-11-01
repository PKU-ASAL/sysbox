package controlstore

import (
	"time"

	"github.com/oslab/sysbox/pkg/model"
)

type SupervisorPolicy string

type HealthSnapshot struct {
	Topology  string               `json:"topology"`
	Observed  time.Time            `json:"observed_at"`
	Health    model.TopologyHealth `json:"health"`
	Policy    SupervisorPolicy     `json:"policy"`
	AutoHeal  bool                 `json:"auto_heal"`
	Action    string               `json:"action,omitempty"`
	RunID     string               `json:"run_id,omitempty"`
	Recovered []string             `json:"recovered_runs,omitempty"`
	LastError string               `json:"last_error,omitempty"`
}
