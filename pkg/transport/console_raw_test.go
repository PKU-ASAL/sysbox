package transport

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestRawConsoleSessionWaitDoesNotConsumeOutput(t *testing.T) {
	host, guest := net.Pipe()
	session := NewRawConsoleSession(host)
	defer session.Close()
	type waitResult struct {
		code int
		err  error
	}
	done := make(chan waitResult, 1)
	go func() {
		code, err := session.Wait()
		done <- waitResult{code: code, err: err}
	}()
	message := []byte("console output must reach stdout")
	written := make(chan error, 1)
	go func() {
		_, err := guest.Write(message)
		if err == nil {
			err = guest.Close()
		}
		written <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	read := make(chan []byte, 1)
	readErr := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(session.Stdout())
		read <- data
		readErr <- err
	}()
	select {
	case data := <-read:
		if string(data) != string(message) {
			t.Fatalf("stdout = %q, want %q", data, message)
		}
		if err := <-readErr; err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stdout read timed out")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.code != 0 {
			t.Fatalf("wait = (%d, %v), want (0, nil)", result.code, result.err)
		}
	case <-ctx.Done():
		t.Fatal("wait timed out")
	}
}
