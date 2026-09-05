package controlplane

import "time"

// GlobalRevision is a content-addressed HCL blob, decoupled from any topology.
//
// CreatedAt is the last-publish time, not the first-seen time: republishing
// identical HCL overwrites the stored record, so the timestamp records the most
// recent publish of that content.
type GlobalRevision struct {
	Revision  string    `json:"revision"`
	HCL       string    `json:"hcl"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}
