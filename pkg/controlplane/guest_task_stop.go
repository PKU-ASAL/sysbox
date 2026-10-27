package controlplane

// GuestTaskStop routes stop-only authority to its canonical logical resource.
// Hosts must still verify the provider's UID/generation before contacting it.
type GuestTaskStop struct {
	Topology     string                      `json:"topology"`
	Node         string                      `json:"node"`
	Cancellation SignedExecutionCancellation `json:"cancellation"`
}
type GuestTaskStopCursor struct {
	AfterEpoch   uint64 `json:"after_epoch,omitempty"`
	ThroughEpoch uint64 `json:"through_epoch,omitempty"`
}
type GuestTaskStopIssue struct {
	Epoch    uint64 `json:"epoch"`
	ActionID string `json:"action_id"`
}
type GuestTaskStopPage struct {
	Stops        []GuestTaskStop      `json:"stops"`
	Issues       []GuestTaskStopIssue `json:"issues,omitempty"`
	NextEpoch    uint64               `json:"next_epoch,omitempty"`
	ThroughEpoch uint64               `json:"through_epoch"`
}

const GuestTaskStopPageSize = 64
