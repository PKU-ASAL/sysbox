package api

type CleanupReport struct {
	RunID      string          `json:"run_id"`
	Topology   string          `json:"topology,omitempty"`
	Containers []CleanupAction `json:"containers,omitempty"`
	Networks   []CleanupAction `json:"networks,omitempty"`
	MicroVMs   []CleanupAction `json:"microvms,omitempty"`
}

type CleanupAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Class      string `json:"-"`
}

func appendCleanupAction(report *CleanupReport, action CleanupAction) {
	switch action.Class {
	case "network":
		report.Networks = append(report.Networks, action)
	case "microvm":
		report.MicroVMs = append(report.MicroVMs, action)
	default:
		report.Containers = append(report.Containers, action)
	}
}
