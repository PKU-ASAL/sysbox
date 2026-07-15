package buildinfo

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentReturnsBuildVariablesAndGoVersion(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = originalVersion, originalCommit, originalBuildTime })
	Version, Commit, BuildTime = "v0.1.0", "0123456789abcdef", "2026-07-15T00:00:00Z"

	got := Current()
	require.Equal(t, "v0.1.0", got.Version)
	require.Equal(t, "0123456789abcdef", got.Commit)
	require.Equal(t, "2026-07-15T00:00:00Z", got.BuildTime)
	require.True(t, strings.HasPrefix(got.GoVersion, "go"))
}

func TestInfoJSONUsesStableFieldNames(t *testing.T) {
	raw, err := json.Marshal(Info{Version: "dev", Commit: "unknown", BuildTime: "unknown", GoVersion: "go1.test"})
	require.NoError(t, err)
	require.JSONEq(t, `{"version":"dev","commit":"unknown","build_time":"unknown","go_version":"go1.test"}`, string(raw))
}
