package agentexec

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLeaseRenewalFailureCancelsExecution(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "owner lost", http.StatusConflict) }))
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &controlplane.Run{ID: "run", AgentID: "agent", LeaseOwner: "incarnation:claim", LeaseUntil: time.Now().Add(time.Second)}
	stop := startRunLeaseRenewal(ctx, Options{APIURL: api.URL, ID: "agent", RunRenewInterval: time.Millisecond, RunRenewTTL: time.Second}, run, cancel)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lease loss did not stop execution")
	}
	require.Error(t, ctx.Err())
}

func TestRenewalPreservesShortAPILeaseBudget(t *testing.T) {
	requests := make(chan int, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			TTL int `json:"ttl_seconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		requests <- request.TTL
		_ = json.NewEncoder(w).Encode(controlplane.Run{ID: "run", LeaseOwner: "owner", Attempt: 1, LeaseUntil: time.Now().Add(time.Duration(request.TTL) * time.Second)})
	}))
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startRunLeaseRenewal(ctx, Options{APIURL: api.URL, ID: "agent", RunRenewTTL: 30 * time.Minute, RunRenewInterval: time.Millisecond}, &controlplane.Run{ID: "run", LeaseOwner: "owner", Attempt: 1, LeaseUntil: time.Now().Add(10 * time.Second)}, cancel)
	defer stop()
	select {
	case ttl := <-requests:
		stop()
		require.Greater(t, ttl, 0)
		require.LessOrEqual(t, ttl, 10)
	case <-time.After(time.Second):
		t.Fatal("no renewal")
	}
}
