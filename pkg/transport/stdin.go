package transport

import (
	"bytes"
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"io"
	"strings"
)

// Stdin buffering must not outlive the execution context. Memory readers are
// finite; streaming readers must implement Close that unblocks Read on cancel.
func readVsockStdin(ctx context.Context, reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	switch r := reader.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader:
	case io.ReadCloser:
		defer abortConnOnContext(ctx, r)()
	default:
		return nil, fmt.Errorf("vsock streaming stdin must be a cancellable ReadCloser: %w", substrate.ErrNotSupported)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, vsockrpc.MaxStdinBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > vsockrpc.MaxStdinBytes {
		return nil, fmt.Errorf("vsock stdin exceeds %d bytes", vsockrpc.MaxStdinBytes)
	}
	return raw, nil
}
