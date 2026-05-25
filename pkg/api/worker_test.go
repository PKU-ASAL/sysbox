package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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
