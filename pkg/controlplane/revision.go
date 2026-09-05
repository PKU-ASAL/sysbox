package controlplane

import "time"

// GlobalRevision is a content-addressed HCL blob, decoupled from any topology.
type GlobalRevision struct {
	Revision  string    `json:"revision"`
	HCL       string    `json:"hcl"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}
