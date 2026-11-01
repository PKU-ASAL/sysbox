package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/stretchr/testify/require"
)

func TestRunLeaseClaimChild(t *testing.T) {
	root := os.Getenv("SYSBOX_TEST_RUN_CLAIM_ROOT")
	if root == "" {
		return
	}
	s := &controlstore.Local{RunsDir: root}
	fmt.Println("ready")
	var token [1]byte
	_, err := os.Stdin.Read(token[:])
	require.NoError(t, err)
	var won bool
	if os.Getenv("SYSBOX_TEST_CLAIM_KIND") == "command" {
		_, won, err = s.AcquireAgentCommandLease(context.Background(), "agent", "shared", os.Getenv("SYSBOX_TEST_RUN_CLAIM_OWNER"), time.Minute)
	} else {
		_, won, err = s.ClaimRun(context.Background(), "shared", "agent", os.Getenv("SYSBOX_TEST_RUN_CLAIM_OWNER"), time.Minute)
	}
	require.NoError(t, err)
	fmt.Printf("won=%t\n", won)
}
func TestRunLeaseHasOneWinnerAcrossProcesses(t *testing.T) {
	assertLeaseHasOneWinnerAcrossProcesses(t, "run")
}
func TestCommandLeaseHasOneWinnerAcrossProcesses(t *testing.T) {
	assertLeaseHasOneWinnerAcrossProcesses(t, "command")
}
func assertLeaseHasOneWinnerAcrossProcesses(t *testing.T, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	store := &controlstore.Local{RunsDir: root}
	if kind == "command" {
		require.NoError(t, store.SaveAgentCommand(ctx, controlplane.AgentCommand{ID: "shared", AgentID: "agent", Type: "node_operation"}))
	} else {
		require.NoError(t, store.SaveRun(ctx, controlplane.Run{ID: "shared", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}))
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	const count = 3
	ready := make(chan bool, count)
	results := make(chan string, count)
	done := make(chan error, count)
	var inputs []io.WriteCloser
	for i := 0; i < count; i++ {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRunLeaseClaimChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), "SYSBOX_TEST_RUN_CLAIM_ROOT="+root, fmt.Sprintf("SYSBOX_TEST_RUN_CLAIM_OWNER=worker-%d", i))
		cmd.Env = append(cmd.Env, "SYSBOX_TEST_CLAIM_KIND="+kind)
		out, err := cmd.StdoutPipe()
		require.NoError(t, err)
		in, err := cmd.StdinPipe()
		require.NoError(t, err)
		inputs = append(inputs, in)
		require.NoError(t, cmd.Start())
		go func() {
			scanner := bufio.NewScanner(out)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "ready" {
					ready <- true
				}
				if strings.HasPrefix(line, "won=") {
					results <- line
				}
			}
			done <- cmd.Wait()
		}()
	}
	for i := 0; i < count; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("claim worker failed to become ready")
		}
	}
	for _, input := range inputs {
		_, err := input.Write([]byte{1})
		require.NoError(t, err)
		require.NoError(t, input.Close())
	}
	for i := 0; i < count; i++ {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("claim worker timed out")
		}
	}
	close(results)
	winners := 0
	responses := 0
	for result := range results {
		responses++
		if result == "won=true" {
			winners++
		}
	}
	require.Equal(t, count, responses)
	require.Equal(t, 1, winners)
	if kind == "command" {
		got, err := store.ListAgentCommands(ctx, "agent")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, 1, got[0].Attempt)
		require.Equal(t, controlplane.AgentCommandStatusLeased, got[0].Status)
		return
	}
	got, err := store.GetRun(ctx, "shared")
	require.NoError(t, err)
	require.Equal(t, 1, got.Attempt)
	require.Equal(t, controlplane.RunRunning, got.Status)
}
func TestRunLeaseHasOneWinnerAcrossStoreInstances(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s controlstore.Store) {
		ctx := context.Background()
		require.NoError(t, s.SaveRun(ctx, controlplane.Run{ID: "claim", Topology: "lab", AgentID: "agent", Status: controlplane.RunAssigned}))
		other := s
		if local, ok := s.(*controlstore.Local); ok {
			other = &controlstore.Local{RunsDir: local.RunsDir}
		}
		if db, ok := s.(*controlstore.SQLite); ok {
			copy := &controlstore.SQLite{DbPath: db.DbPath, RunsDir: db.RunsDir}
			pool, err := copy.OpenDB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			other = copy
		}
		start := make(chan struct{})
		results := make(chan bool, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for i, store := range []controlstore.Store{s, other} {
			wg.Go(func() {
				<-start
				_, ok, err := store.ClaimRun(ctx, "claim", "agent", fmt.Sprintf("worker-%d", i), time.Minute)
				results <- ok
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		winners := 0
		for won := range results {
			if won {
				winners++
			}
		}
		for err := range errs {
			require.NoError(t, err)
		}
		require.Equal(t, 1, winners)
	})
}
