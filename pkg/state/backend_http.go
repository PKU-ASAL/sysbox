package state

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
)

var httpClient = &http.Client{}

func newRequest(ctx context.Context, method, url string, body []byte, headers map[string]string) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

func readBody(resp *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB max
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return data, nil
}

// ── S3 minimal client (no AWS SDK dependency) ─────────────────────────────────
//
// Uses the `aws s3api` CLI for get/put so we avoid a heavy SDK dependency.
// This is sufficient for state persistence which happens infrequently.

func (b *S3Backend) s3Client(_ context.Context) (string, error) {
	// Verify aws CLI is available.
	if _, err := exec.LookPath("aws"); err != nil {
		return "", fmt.Errorf("s3 backend requires the 'aws' CLI in PATH")
	}
	return "aws", nil
}

func s3GetObject(ctx context.Context, _ string, bucket, key string) ([]byte, error) {
	args := []string{"s3", "api", "get-object",
		"--bucket", bucket,
		"--key", key,
		"-",
	}
	// aws s3api get-object writes to a file; for stdout, use s3 cp instead.
	args = []string{"s3", "cp",
		fmt.Sprintf("s3://%s/%s", bucket, key),
		"-",
	}
	return runAWS(ctx, args)
}

func s3PutObject(ctx context.Context, _ string, bucket, key string, data []byte) error {
	// aws s3 cp from stdin
	tmp, err := os.CreateTemp("", "sysbox-state-s3-*")
	if err != nil {
		return fmt.Errorf("s3 put: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("s3 put: write temp: %w", err)
	}
	tmp.Close()

	args := []string{"s3", "cp",
		tmp.Name(),
		fmt.Sprintf("s3://%s/%s", bucket, key),
	}
	_, err = runAWS(ctx, args)
	return err
}

func runAWS(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("aws %v: %w\n%s", args, err, out)
	}
	return out, nil
}
