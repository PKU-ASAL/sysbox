package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileRejectsUnverifiedBodyWithoutTruncatingDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
	for _, size := range []int64{-1, 5} {
		var output bytes.Buffer
		handleWriteFile(&output, bufio.NewReader(strings.NewReader("bad")), vsockrpc.Request{Path: path, Size: size})
		var frame vsockrpc.Frame
		require.NoError(t, json.Unmarshal(output.Bytes(), &frame))
		require.NotEmpty(t, frame.Error)
		actual, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "original", string(actual))
	}
}

func TestWriteFileVerifiesBodyBeforeReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
	for _, body := range []string{"truncated", "wrong-body-longer", "replacement"} {
		var output bytes.Buffer
		want := "replacement"
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(want)))
		handleWriteFile(&output, bufio.NewReader(strings.NewReader(body)), vsockrpc.Request{Path: path, Size: int64(len(want)), SHA256: hash, Mode: 0640})
		var frame vsockrpc.Frame
		require.NoError(t, json.Unmarshal(output.Bytes(), &frame))
		actual, err := os.ReadFile(path)
		require.NoError(t, err)
		if body == want {
			require.Empty(t, frame.Error)
			require.Equal(t, hash, frame.SHA256)
			require.Equal(t, want, string(actual))
		} else {
			require.NotEmpty(t, frame.Error)
			require.Equal(t, "original", string(actual))
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		require.NoError(t, err)
		require.Len(t, entries, 1)
	}
}

func TestExecReceivesStdinAndRejectsTruncationBeforeEffects(t *testing.T) {
	var output bytes.Buffer
	w := &mutexWriter{Writer: &output}
	handleExec(w, strings.NewReader("stdin\x00data"), vsockrpc.Request{Cmd: []string{"cat"}, StdinSize: 10})
	decoder := json.NewDecoder(&output)
	var collected []byte
	for {
		var f vsockrpc.Frame
		require.NoError(t, decoder.Decode(&f))
		collected = append(collected, f.Stdout...)
		if f.Done {
			require.Empty(t, f.Error)
			break
		}
	}
	require.Equal(t, "stdin\x00data", string(collected))
	path := filepath.Join(t.TempDir(), "must-not-exist")
	output.Reset()
	handleExec(w, strings.NewReader("short"), vsockrpc.Request{Cmd: []string{"touch", path}, StdinSize: 10})
	var f vsockrpc.Frame
	require.NoError(t, json.Unmarshal(output.Bytes(), &f))
	require.NotEmpty(t, f.Error)
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err))
}

func TestWriteFileRejectsSymlinkEscapeAndPreservesZeroMode(t *testing.T) {
	root := t.TempDir()
	previous := allowedWritePaths
	allowedWritePaths = []string{root + "/"}
	t.Cleanup(func() { allowedWritePaths = previous })
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	for _, name := range []string{"escape/file", "zero"} {
		var output bytes.Buffer
		handleWriteFile(&output, bufio.NewReader(strings.NewReader("x")), vsockrpc.Request{Path: filepath.Join(root, name), Size: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("x"))), Mode: 0})
		var f vsockrpc.Frame
		require.NoError(t, json.Unmarshal(output.Bytes(), &f))
		if name == "zero" {
			require.Empty(t, f.Error)
			info, err := os.Stat(filepath.Join(root, name))
			require.NoError(t, err)
			require.Zero(t, info.Mode().Perm())
		} else {
			require.NotEmpty(t, f.Error)
			_, err := os.Stat(filepath.Join(outside, "file"))
			require.True(t, os.IsNotExist(err))
		}
	}
}
