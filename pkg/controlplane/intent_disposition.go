package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// IntentDispositionDecision is an explicit operator choice bound to a read-only
// evidence snapshot. It is not a proof of cessation or a successful Run result.
type IntentDispositionDecision struct {
	Version           int                       `json:"version"`
	ID                string                    `json:"id"`
	FenceID           string                    `json:"fence_id"`
	Topology          string                    `json:"topology"`
	RunID             string                    `json:"run_id"`
	RunQueuedAt       time.Time                 `json:"run_queued_at"`
	RunRevision       string                    `json:"run_revision"`
	ExpectedSerial    int64                     `json:"expected_serial"`
	JournalSHA256     string                    `json:"journal_sha256"`
	ObservationSHA256 string                    `json:"observation_sha256"`
	Reason            string                    `json:"reason"`
	Actions           []IntentDispositionAction `json:"actions"`
}
type IntentDispositionAction struct {
	ActionID    string `json:"action_id"`
	Decision    string `json:"decision"`
	ResourceUID string `json:"resource_uid"`
	Generation  int64  `json:"generation"`
	ExternalID  string `json:"external_id"`
}

func (p IntentDispositionDecision) Digest() (string, error) {
	if p.Version != 1 || !fenceName.MatchString(p.ID) || !fenceName.MatchString(p.FenceID) || !fenceName.MatchString(p.Topology) || !fenceName.MatchString(p.RunID) || p.RunQueuedAt.IsZero() || p.RunRevision == "" || p.ExpectedSerial < 0 || !validFenceDigest(p.JournalSHA256) || !validFenceDigest(p.ObservationSHA256) || strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 4096 || len(p.Actions) == 0 || len(p.Actions) > 1024 {
		return "", fmt.Errorf("invalid intent disposition decision")
	}
	seen := map[string]bool{}
	for _, a := range p.Actions {
		if a.ActionID == "" || seen[a.ActionID] || a.ResourceUID == "" || a.Generation <= 0 || a.ExternalID == "" || (a.Decision != "retain" && a.Decision != "absent") {
			return "", fmt.Errorf("invalid or duplicate intent disposition action")
		}
		seen[a.ActionID] = true
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("sysbox.intent-disposition.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
