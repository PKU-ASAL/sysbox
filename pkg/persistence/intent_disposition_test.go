package persistence

import (
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIntentDispositionChoiceRejectsStaleOrIncompleteEvidence(t *testing.T) {
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", QueuedAt: time.Now().UTC(), Revision: "revision"}
	action := runtime.ActionRecord{ID: "a", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, Resource: address.Resource("sysbox_node", "node"), Action: controlplane.PlanActionCreate, Status: runtime.ActionUncertain, OwnerUID: "owner", ResourceUID: "resource", Generation: 1, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "node"), Driver: "docker", ExternalID: "external", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}}
	actions := []runtime.ActionRecord{action}
	_, journal, err := FenceJournal(run, 3, actions)
	require.NoError(t, err)
	report := controlplane.RunRecoveryReport{RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, TopologyUID: "owner", StateSerial: 3, Mode: controlplane.RunRecoveryObserve, Resources: []controlplane.RecoveredResource{{ActionID: "a", Resource: action.Resource.String(), OwnerUID: "owner", ResourceUID: "resource", Generation: 1, ExternalID: "external", Status: "present", JournalStatus: "uncertain", ProviderScope: "local-only"}}}
	decision := controlplane.IntentDispositionDecision{Version: 1, ID: "decision", FenceID: "fence", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, ExpectedSerial: 3, JournalSHA256: journal, ObservationSHA256: evidenceHash(report), Reason: "preserve resource", Actions: []controlplane.IntentDispositionAction{{ActionID: "a", Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	require.NoError(t, ValidateIntentDispositionChoice(decision, run, 3, actions, report))
	for _, kind := range []string{"serial", "run", "journal", "observation", "choice", "identity", "composite", "reset", "duplicate", "foreign-owner", "missing-coverage"} {
		t.Run(kind, func(t *testing.T) {
			d := decision
			d.Actions = append([]controlplane.IntentDispositionAction(nil), decision.Actions...)
			r := report
			r.Resources = append([]controlplane.RecoveredResource(nil), report.Resources...)
			aa := append([]runtime.ActionRecord(nil), actions...)
			switch kind {
			case "serial":
				d.ExpectedSerial++
			case "run":
				d.RunQueuedAt = d.RunQueuedAt.Add(time.Second)
			case "journal":
				d.JournalSHA256 = evidenceHash("other")
			case "observation":
				d.ObservationSHA256 = evidenceHash("other")
			case "choice":
				d.Actions[0].Decision = "absent"
			case "identity":
				d.Actions[0].ExternalID = "other"
			case "composite":
				aa[0].ReplayForbidden = true
			case "reset":
				aa[0].Action = controlplane.PlanActionReset
			case "duplicate":
				r.Resources = append(r.Resources, r.Resources[0])
			case "foreign-owner":
				aa[0].OwnerUID = "foreign"
			case "missing-coverage":
				r.Resources = nil
			}
			if kind == "composite" || kind == "reset" || kind == "foreign-owner" {
				_, d.JournalSHA256, err = FenceJournal(run, 3, aa)
				require.NoError(t, err)
			}
			if kind == "duplicate" || kind == "missing-coverage" {
				d.ObservationSHA256 = evidenceHash(r)
			}
			require.Error(t, ValidateIntentDispositionChoice(d, run, 3, aa, r))
		})
	}
}

func TestIntentDispositionChoiceChecksSettledActionObservation(t *testing.T) {
	run := controlplane.Run{ID: "run", Topology: "lab", AgentID: "agent", QueuedAt: time.Now().UTC(), Revision: "revision"}
	old := runtime.ActionRecord{ID: "done", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, OwnerUID: "owner", ResourceUID: "done", Generation: 1, Resource: address.Resource("sysbox_node", "done"), Status: runtime.ActionSucceeded, Action: controlplane.PlanActionCreate, Candidate: &state.Resource{Address: address.Resource("sysbox_node", "done"), Driver: "docker", ExternalID: "done-id", OwnerUID: "owner", ResourceUID: "done", Generation: 1}}
	pending := old
	pending.ID = "pending"
	pending.Status = runtime.ActionUncertain
	actions := []runtime.ActionRecord{old, pending}
	_, journal, err := FenceJournal(run, 3, actions)
	require.NoError(t, err)
	report := controlplane.RunRecoveryReport{RunID: run.ID, AgentID: run.AgentID, QueuedAt: run.QueuedAt, Revision: run.Revision, TopologyUID: "owner", StateSerial: 3, Mode: controlplane.RunRecoveryObserve, Resources: []controlplane.RecoveredResource{{ActionID: "done", Resource: old.Resource.String(), OwnerUID: "foreign", ResourceUID: "done", Generation: 1, ExternalID: "done-id", Status: "present", JournalStatus: "succeeded", ProviderScope: "local-only"}, {ActionID: "pending", Resource: pending.Resource.String(), OwnerUID: "owner", ResourceUID: "done", Generation: 1, ExternalID: "done-id", Status: "present", JournalStatus: "uncertain", ProviderScope: "local-only"}}}
	d := controlplane.IntentDispositionDecision{Version: 1, ID: "decision", FenceID: "fence", Topology: "lab", RunID: run.ID, RunQueuedAt: run.QueuedAt, RunRevision: run.Revision, ExpectedSerial: 3, JournalSHA256: journal, ObservationSHA256: evidenceHash(report), Reason: "retain", Actions: []controlplane.IntentDispositionAction{{ActionID: "pending", Decision: "retain", ResourceUID: "done", Generation: 1, ExternalID: "done-id"}}}
	require.Error(t, ValidateIntentDispositionChoice(d, run, 3, actions, report))
}
