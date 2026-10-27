package hostsupervisor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRunRecoveryAcceptsOnlyCommandLeaseProof(t *testing.T) {
	called := 0
	s := &Server{RunRecovery: func(_ context.Context, proof RunRecoveryRequest) (*controlplane.RunRecoveryReport, error) {
		called++
		return &controlplane.RunRecoveryReport{CommandID: proof.CommandID, RunID: "canonical"}, nil
	}}
	proof := RunRecoveryRequest{CommandID: "command", LeaseOwner: "worker", Attempt: 1}
	got := s.Handle(context.Background(), Request{Version: 1, Operation: "run-recovery", RecoveryRequest: &proof})
	require.Empty(t, got.Error)
	require.Equal(t, "canonical", got.RecoveryReport.RunID)
	proof.CommandID = "../other"
	got = s.Handle(context.Background(), Request{Version: 1, Operation: "run-recovery", RecoveryRequest: &proof})
	require.NotEmpty(t, got.Error)
	require.Equal(t, 1, called)
}

func TestRecoveryAuthorizationAcceptsPublishedRevisionAboveOneMiB(t *testing.T) {
	revision := &controlplane.GlobalRevision{Files: map[string][]byte{"large.txt": []byte(strings.Repeat("x", 1<<20))}}
	revision.Revision = controlplane.ComputeRevisionDigest(revision.Files)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(controlplane.RunRecoveryAuthorization{Revision: revision})
	}))
	defer server.Close()
	client := AuthorityClient{Identity: agent.Identity{ID: "agent", APIURL: server.URL}}
	got, err := client.AuthorizeRunRecovery(context.Background(), RunRecoveryRequest{CommandID: "recover", LeaseOwner: "worker", Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, revision, got.Revision)
}

func TestRunRecoveryRPCUsesAuthorizedBudgetInsteadOfTransportDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	want, _ := parent.Deadline()
	observed := make(chan time.Time, 1)
	s := &Server{RunRecovery: func(ctx context.Context, proof RunRecoveryRequest) (*controlplane.RunRecoveryReport, error) {
		deadline, _ := ctx.Deadline()
		observed <- deadline
		return &controlplane.RunRecoveryReport{CommandID: proof.CommandID}, nil
	}}
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); s.serveConnection(parent, server) }()
	proof := RunRecoveryRequest{CommandID: "command", LeaseOwner: "worker", Attempt: 1}
	require.NoError(t, json.NewEncoder(client).Encode(Request{Version: 1, Operation: "run-recovery", RecoveryRequest: &proof}))
	var response Response
	require.NoError(t, json.NewDecoder(client).Decode(&response))
	<-done
	require.Empty(t, response.Error)
	require.Equal(t, want, <-observed, "host callback applies canonical command lease; transport must not truncate it")
}
