package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderAgentID        = "X-Sysbox-Agent-ID"
	HeaderAgentTimestamp = "X-Sysbox-Agent-Timestamp"
	HeaderAgentSignature = "X-Sysbox-Agent-Signature"
)

func SecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func SignRequest(req *http.Request, agentID, secret string, now time.Time) error {
	if req == nil {
		return fmt.Errorf("request is required")
	}
	if agentID == "" || secret == "" {
		return nil
	}
	body, err := snapshotBody(req)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(now.UTC().Unix(), 10)
	req.Header.Set(HeaderAgentID, agentID)
	req.Header.Set(HeaderAgentTimestamp, ts)
	req.Header.Set(HeaderAgentSignature, Signature(req.Method, req.URL.RequestURI(), ts, body, secret))
	return nil
}

func Signature(method, requestURI, timestamp string, body []byte, secret string) string {
	bodySum := sha256.Sum256(body)
	canonical := strings.Join([]string{
		strings.ToUpper(method),
		requestURI,
		timestamp,
		hex.EncodeToString(bodySum[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func snapshotBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return body, nil
}
