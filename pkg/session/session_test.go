package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/sensor"
)

func TestLabelerAnnotate(t *testing.T) {
	l := NewLabeler()
	l.RegisterSession(42, "sess-abc")

	e := &sensor.Event{CgroupID: 42}
	l.Annotate(e)
	require.Equal(t, "sess-abc", e.SessionID)
	require.True(t, e.IsAttack)

	// Different cgroup: no session.
	e2 := &sensor.Event{CgroupID: 99}
	l.Annotate(e2)
	require.Empty(t, e2.SessionID)
	require.False(t, e2.IsAttack)
}

func TestLabelerUnregister(t *testing.T) {
	l := NewLabeler()
	l.RegisterSession(7, "s1")
	l.UnregisterSession(7)

	e := &sensor.Event{CgroupID: 7}
	l.Annotate(e)
	require.Empty(t, e.SessionID)
}

func TestRegistryRegisterAndResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	reg := NewRegistry(path)

	exp := Expectation{
		NodeID:    "node_a",
		SourceIP:  "10.0.0.1",
		SessionID: "exp-abc",
		ExpiresAt: time.Now().Add(60 * time.Second),
	}
	require.NoError(t, reg.Register(exp))

	// Match.
	sid := reg.Resolve("node_a", "10.0.0.1")
	require.Equal(t, "exp-abc", sid)

	// Already consumed.
	sid2 := reg.Resolve("node_a", "10.0.0.1")
	require.Empty(t, sid2)
}

func TestRegistryExpired(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(filepath.Join(dir, "r.json"))

	exp := Expectation{
		NodeID:    "node_a",
		SessionID: "exp-xyz",
		ExpiresAt: time.Now().Add(-1 * time.Second), // already expired
	}
	require.NoError(t, reg.Register(exp))

	sid := reg.Resolve("node_a", "any")
	require.Empty(t, sid)
}

func TestRegistryAnySource(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(filepath.Join(dir, "r.json"))

	require.NoError(t, reg.Register(Expectation{
		NodeID:    "node_b",
		SourceIP:  "", // match any
		SessionID: "wildcard",
		ExpiresAt: time.Now().Add(time.Minute),
	}))

	sid := reg.Resolve("node_b", "1.2.3.4")
	require.Equal(t, "wildcard", sid)
}
