package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestMetadataHTTPCorruptionIsNotNotFound(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	for _, tc := range []struct{ directory, path string }{{"_guest-executions", "/v1/executions/"}, {"_guest-file-operations", "/v1/file-operations/"}} {
		t.Run(tc.directory, func(t *testing.T) {
			dir := filepath.Join(s.runsDir, tc.directory)
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"secret":"never-leak-this",broken`), 0600))
			for _, test := range []struct {
				id, role string
				code     int
			}{{"broken", "admin", 500}, {"missing", "admin", 404}, {"broken", "viewer", 403}} {
				r := httptest.NewRequest(http.MethodGet, tc.path+test.id, nil)
				r.Header.Set("X-Sysbox-Roles", test.role)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				require.Equal(t, test.code, w.Code, w.Body.String())
				require.NotContains(t, w.Body.String(), "never-leak-this")
			}
		})
	}
}
