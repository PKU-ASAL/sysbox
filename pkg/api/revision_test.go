package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func publishRevision(t *testing.T, s *Server, hcl string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBufferString(hcl))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body["revision"]
}

func TestPublishRevisionIsContentAddressed(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	first := publishRevision(t, s, `resource "sysbox_node" "web" {}`)
	second := publishRevision(t, s, `resource "sysbox_node" "web" {}`)

	require.NotEmpty(t, first)
	require.True(t, strings.HasPrefix(first, "sha256:"))
	require.Equal(t, first, second, "同一份 HCL 必须返回同一个 digest")
}

func TestPublishRevisionDifferentContentGivesDifferentDigest(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	require.NotEqual(t,
		publishRevision(t, s, `resource "sysbox_node" "web" {}`),
		publishRevision(t, s, `resource "sysbox_node" "web2" {}`),
	)
}
