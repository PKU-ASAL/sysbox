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

func publishRevisionFiles(t *testing.T, s *Server, files map[string]string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"files": files})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp["revision"]
}

// publishRevision publishes a single-file project rooted at field.sysbox.hcl,
// preserving the old test call sites.
func publishRevision(t *testing.T, s *Server, hcl string) string {
	t.Helper()
	return publishRevisionFiles(t, s, map[string]string{"field.sysbox.hcl": hcl})
}

func TestPublishRevisionIsContentAddressed(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	hcl := `resource "sysbox_node" "web" {}`
	first := publishRevisionFiles(t, s, map[string]string{"field.sysbox.hcl": hcl})
	second := publishRevisionFiles(t, s, map[string]string{"field.sysbox.hcl": hcl})

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

func TestPublishRevisionTreeDigestIncludesPaths(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	require.NotEqual(t,
		publishRevisionFiles(t, s, map[string]string{"a": "x"}),
		publishRevisionFiles(t, s, map[string]string{"b": "x"}),
		"same content at different paths must produce different digests")
}

func TestPublishRevisionTreeStableDigest(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	files := map[string]string{
		"field.sysbox.hcl":     `resource "sysbox_node" "web" {}`,
		"modules/web/main.hcl": `resource "sysbox_node" "web" {}`,
		"files/f.txt":          "hello",
	}
	first := publishRevisionFiles(t, s, files)
	second := publishRevisionFiles(t, s, files)
	require.Equal(t, first, second, "the same tree must publish to the same digest")
}

func TestPublishRevisionRejectsTraversal(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	for _, path := range []string{"../evil", "a/../../evil", "/etc/passwd", `a\b`, ".", "..", "a//b", "a/./b", "a/../b"} {
		t.Run(path, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"files": map[string]string{path: "x"}})
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestPublishRevisionRequiresFiles(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBufferString(`{"files":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
