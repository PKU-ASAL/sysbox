package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestWorkerRegistry(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workers", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"local"`)

	body := bytes.NewBufferString(`{
  "id": "host-a",
  "capabilities": ["docker", "network", "kvm"],
  "labels": {"role": "lab"},
  "version": "dev"
}`)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/workers", body))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"host-a"`)
	require.Contains(t, rec.Body.String(), `"status":"online"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workers/host-a", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"version":"dev"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/workers/host-a/heartbeat", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"online"`)

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workers", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed struct {
		Workers []struct {
			ID string `json:"id"`
		} `json:"workers"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed.Workers, 2)
	require.ElementsMatch(t, []string{"host-a", "local"}, []string{listed.Workers[0].ID, listed.Workers[1].ID})
}

func TestRunDefaultsToLocalWorker(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	run := s.jobs.start("mixed", "apply")
	require.Equal(t, DefaultWorkerID, run.WorkerID)

	stored, err := s.apiStore.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, DefaultWorkerID, stored.WorkerID)
}

func TestAgentStreamReceivesAssignedRun(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.agents.Save(controlplane.Worker{ID: "host-a", Status: "online", Capabilities: []string{"docker"}})
	server := httptest.NewServer(s)
	defer server.Close()

	errCh := make(chan error, 1)
	bodyCh := make(chan string, 1)
	go func() {
		resp, err := http.Get(server.URL + "/v1/agents/host-a/stream")
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		data := make([]byte, 4096)
		n, err := resp.Body.Read(data)
		if err != nil && err != io.EOF {
			errCh <- err
			return
		}
		bodyCh <- string(data[:n])
	}()

	time.Sleep(50 * time.Millisecond)
	run := s.jobs.start("mixed", "apply")
	require.NoError(t, s.dispatchRun(context.Background(), run, []string{"docker"}))

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case body := <-bodyCh:
		require.Contains(t, body, "data:")
		require.Contains(t, body, "run_assigned")
		require.Contains(t, body, run.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent stream command")
	}
}
