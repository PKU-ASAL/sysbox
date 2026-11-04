package hostsupervisor

import (
	"context"
	"github.com/oslab/sysbox/pkg/buildinfo"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBuildInfoRequiresVersionAndNoTarget(t *testing.T) {
	server := &Server{}
	for _, request := range []Request{{Operation: "version"}, {Version: 1, Operation: "version", Target: TargetRef{Topology: "lab"}}} {
		require.NotEmpty(t, server.Handle(context.Background(), request).Error)
	}
	out := server.Handle(context.Background(), Request{Version: 1, Operation: "version"})
	require.Empty(t, out.Error)
	require.Equal(t, buildinfo.Version, out.BuildVersion)
	require.Equal(t, buildinfo.Commit, out.BuildCommit)
}
