package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

type legacyIntentAudit struct {
	ID           string `json:"id"`
	RunID        string `json:"run_id,omitempty"`
	Status       string `json:"status"`
	ExternalID   string `json:"external_id,omitempty"`
	OperationID  string `json:"operation_id,omitempty"`
	Resolution   string `json:"resolution"`
	RecordSHA256 string `json:"record_sha256"`
}

// Offline forensic inspection deliberately does not open the repository or
// run its compatibility/migration code. The source is evidence, never authority.
func newHistoricalAuditCommand() *cobra.Command {
	return &cobra.Command{Use: "audit-intents <aggregate.json>", Short: "Read a legacy action journal without migration or execution", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		info, err := os.Lstat(args[0])
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 32<<20 {
			return fmt.Errorf("source must be a regular JSON file at most 32MiB")
		}
		file, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (32<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 32<<20 {
			return fmt.Errorf("source exceeds audit limit")
		}
		var header struct {
			Version int `json:"version"`
			Format  int `json:"format"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return err
		}
		if header.Version == 2 && header.Format == 0 {
			return auditHostIntents(cmd, raw)
		}
		var source struct {
			Format  int                        `json:"format"`
			Actions map[string]json.RawMessage `json:"actions"`
			State   struct {
				TopologyUID string `json:"topology_uid"`
			} `json:"state"`
		}
		if json.Unmarshal(raw, &source) != nil || source.Format < 4 || source.Format > 6 || source.Actions == nil || source.State.TopologyUID == "" {
			return fmt.Errorf("expected lifecycle aggregate format4,5 or6")
		}
		out := []legacyIntentAudit{}
		for key, value := range source.Actions {
			var a struct {
				ID          string `json:"id"`
				RunID       string `json:"run_id"`
				Status      string `json:"status"`
				OperationID string `json:"provider_operation_id"`
				Candidate   *struct {
					ExternalID string `json:"external_id"`
				} `json:"candidate"`
			}
			if json.Unmarshal(value, &a) != nil || a.ID != key || a.RunID == "" {
				return fmt.Errorf("invalid action identity")
			}
			if a.Status == "succeeded" {
				continue
			}
			if a.Status != "uncertain" && a.Status != "blocked" && a.Status != "intent_committed" {
				return fmt.Errorf("invalid action status")
			}
			item := legacyIntentAudit{ID: a.ID, RunID: a.RunID, Status: a.Status, OperationID: a.OperationID, Resolution: "manual_reconciliation_required", RecordSHA256: fmt.Sprintf("%x", sha256.Sum256(value))}
			if a.Candidate != nil {
				item.ExternalID = a.Candidate.ExternalID
			}
			out = append(out, item)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"source_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "format": source.Format, "topology_uid": source.State.TopologyUID, "read_only": true, "intents": out})
	}}
}
func init() { stateCmd.AddCommand(newHistoricalAuditCommand()) }

func auditHostIntents(cmd *cobra.Command, raw []byte) error {
	var source struct {
		Version     int                        `json:"version"`
		TopologyUID string                     `json:"topology_uid"`
		Outcomes    map[string]json.RawMessage `json:"outcomes"`
	}
	if json.Unmarshal(raw, &source) != nil || source.Version != 2 || source.TopologyUID == "" || source.Outcomes == nil {
		return fmt.Errorf("invalid host effect ledger")
	}
	out := []map[string]any{}
	keys := make([]string, 0, len(source.Outcomes))
	for key := range source.Outcomes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := source.Outcomes[key]
		var record struct {
			State string `json:"state"`
			Grant struct {
				ActionID  string `json:"action_id"`
				Operation string `json:"operation"`
				BootID    string `json:"target_boot_id"`
			} `json:"grant"`
			Admission  json.RawMessage `json:"run_admission"`
			Result     json.RawMessage `json:"result"`
			Checkpoint json.RawMessage `json:"run_checkpoint"`
		}
		if json.Unmarshal(value, &record) != nil || record.Grant.ActionID == "" {
			return fmt.Errorf("invalid host outcome identity")
		}
		if record.State == "complete" {
			continue
		}
		if record.State != "intent" {
			return fmt.Errorf("unknown host outcome state")
		}
		blockers := []string{"provider_inflight_stop_unproven"}
		if len(record.Admission) == 0 || string(record.Admission) == "null" {
			blockers = append(blockers, "missing_admission_evidence")
		}
		out = append(out, map[string]any{"key": key, "action_id": record.Grant.ActionID, "operation": record.Grant.Operation, "original_boot_id": record.Grant.BootID, "record_sha256": fmt.Sprintf("%x", sha256.Sum256(value)), "resolution": "manual_reconciliation_required", "blockers": blockers})
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"source_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "format": "host-effect-v2", "topology_uid": source.TopologyUID, "read_only": true, "intents": out})
}
