package controlplane

import "testing"

func TestFromRunCommandBuildsSingleResourceExecutionProof(t *testing.T) {
	cmd := AgentCommand{
		Type:        "run_assigned",
		TopologyUID: "topology-uid",
		ActionID:    "action-1",
		ResourceUID: "resource-1",
		Generation:  2,
		Run:         &Run{ID: "run-1", Topology: "lab", Revision: "revision-1", Op: "apply", Target: "sysbox_node.web"},
	}
	request, err := FromRunCommand(cmd, "topology-uid", 7)
	if err != nil {
		t.Fatalf("FromRunCommand() error = %v", err)
	}
	if request.Action != "apply" || request.Target != "sysbox_node.web" || request.ExpectedSerial != 7 {
		t.Fatalf("unexpected request: %+v", request)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("request.Validate() error = %v", err)
	}
}

func TestFromRunCommandRejectsUnfencedRun(t *testing.T) {
	base := AgentCommand{
		Type:        "run_assigned",
		TopologyUID: "topology-uid",
		ActionID:    "action-1",
		ResourceUID: "resource-1",
		Generation:  1,
		Run:         &Run{ID: "run-1", Topology: "lab", Revision: "revision-1", Op: "apply", Target: "sysbox_node.web"},
	}
	checks := []struct {
		name string
		edit func(*AgentCommand)
	}{
		{"missing topology proof", func(cmd *AgentCommand) { cmd.TopologyUID = "" }},
		{"missing resource proof", func(cmd *AgentCommand) { cmd.ResourceUID = "" }},
		{"unsupported operation", func(cmd *AgentCommand) { cmd.Run.Op = "unsupported" }},
		{"multi resource target", func(cmd *AgentCommand) { cmd.Run.Target = "" }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			cmd := base
			if base.Run != nil {
				run := *base.Run
				cmd.Run = &run
			}
			check.edit(&cmd)
			if _, err := FromRunCommand(cmd, "topology-uid", 1); err == nil {
				t.Fatal("FromRunCommand() unexpectedly accepted an unfenced run")
			}
		})
	}
}
