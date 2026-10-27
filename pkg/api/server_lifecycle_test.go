package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/stretchr/testify/require"
)

func lifecycleServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultServiceConfig()
	cfg.Paths.RunsDir = t.TempDir()
	cfg.Paths.WorkspacesDir = t.TempDir()
	cfg.Supervisor.Interval = "0s"
	return NewServerWithConfig(cfg)
}

func TestServerStartContextCancelsActiveRequests(t *testing.T) {
	s := lifecycleServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	s.mux.HandleFunc("GET /lifecycle", func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
	})
	done := make(chan error, 1)
	go func() { done <- s.StartContext(ctx, addr) }()
	client := &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	require.Eventually(t, func() bool {
		resp, err := client.Get("http://" + addr + "/v1/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := client.Get("http://" + addr + "/lifecycle")
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
	<-requestCanceled
	<-requestDone
	select {
	case <-s.supervisor.stop:
	default:
		t.Fatal("supervisor was not stopped")
	}
	listener, err = net.Listen("tcp", addr)
	require.NoError(t, err, "server must release its listener")
	require.NoError(t, listener.Close())
}

func TestServerStartContextPreservesStartupErrors(t *testing.T) {
	t.Run("address in use", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		err = lifecycleServer(t).StartContext(context.Background(), listener.Addr().String())
		require.ErrorIs(t, err, syscall.EADDRINUSE)
	})
	t.Run("initialization", func(t *testing.T) {
		want := errors.New("initialization failed")
		s := &Server{initErr: want}
		require.ErrorIs(t, s.StartContext(context.Background(), "127.0.0.1:0"), want)
	})
}

func TestServerStartContextBoundsShutdown(t *testing.T) {
	s := lifecycleServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	s.mux.HandleFunc("GET /stalled", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release // Model a handler that does not observe cancellation.
	})
	done := make(chan error, 1)
	go func() { done <- s.StartContext(ctx, addr) }()
	client := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	var response *http.Response
	require.Eventually(t, func() bool {
		response, err = client.Get("http://" + addr + "/stalled")
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	defer response.Body.Close()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown did not enforce its deadline")
	}
	_, err = response.Body.Read(make([]byte, 1))
	require.Error(t, err, "shutdown must close stalled request connections")
}
