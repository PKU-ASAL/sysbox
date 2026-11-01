package controlstore

import "time"

type DispatchRetryRecord struct {
	CommandID string    `json:"command_id"`
	Failures  int       `json:"failures"`
	Next      time.Time `json:"next"`
	LastError string    `json:"last_error,omitempty"`
}
