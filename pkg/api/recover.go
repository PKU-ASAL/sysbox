package api

type RecoverReport struct {
	RunID     string          `json:"run_id"`
	Topology  string          `json:"topology,omitempty"`
	Recovered []RecoverAction `json:"recovered,omitempty"`
	Skipped   []RecoverAction `json:"skipped,omitempty"`
}

type RecoverAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}
