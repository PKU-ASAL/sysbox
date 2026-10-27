package agentexec

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimRetriesUnavailablePreservingIncarnation(t *testing.T) {
	expected := controlplane.Run{ID: "run", Revision: "revision", QueuedAt: time.Now().UTC()}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var proof controlplane.RunClaim
		if json.NewDecoder(r.Body).Decode(&proof) != nil || !proof.QueuedAt.Equal(expected.QueuedAt) || proof.Revision != expected.Revision {
			w.WriteHeader(400)
			return
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(expected)
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := claim(ctx, Options{APIURL: s.URL, ID: "agent"}, expected)
	require.NoError(t, err)
	require.Equal(t, expected.ID, got.ID)
	require.EqualValues(t, 3, calls.Load())
}

func TestClaimDoesNotRetryConflict(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(409) }))
	defer s.Close()
	_, err := claim(context.Background(), Options{APIURL: s.URL, ID: "agent"}, controlplane.Run{ID: "run", QueuedAt: time.Now()})
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
}

func TestClaimLostResponseReusesExecutionIdentity(t *testing.T) {
	var calls atomic.Int32
	var identity string
	expected := controlplane.Run{ID: "run", Revision: "revision", QueuedAt: time.Now().UTC()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var proof map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&proof))
		id, _ := proof["claim_id"].(string)
		if id == "" {
			w.WriteHeader(400)
			return
		}
		if calls.Add(1) == 1 {
			identity = id
			_, _ = w.Write([]byte("{"))
			return
		}
		if identity != id {
			w.WriteHeader(409)
			return
		}
		_ = json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := claim(ctx, Options{APIURL: server.URL, ID: "agent"}, expected)
	require.NoError(t, err)
	require.Equal(t, expected.ID, got.ID)
	require.EqualValues(t, 2, calls.Load())
}
