package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/oslab/sysbox/pkg/agent"
)

const agentSignatureSkew = 5 * time.Minute

func (s *Server) verifyAgentRequest(r *http.Request, agentID string) error {
	if agentID == DefaultAgentID {
		return nil
	}
	stored, err := s.agents.Get(agentID)
	if err != nil {
		return err
	}
	if stored.SecretHash == "" {
		return nil
	}
	if stored.AuthSecret == "" {
		return fmt.Errorf("agent secret is not available")
	}
	headerID := r.Header.Get(agent.HeaderAgentID)
	if headerID != agentID {
		return fmt.Errorf("agent id signature mismatch")
	}
	ts := r.Header.Get(agent.HeaderAgentTimestamp)
	sig := r.Header.Get(agent.HeaderAgentSignature)
	if ts == "" || sig == "" {
		return fmt.Errorf("missing agent signature")
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid agent signature timestamp")
	}
	now := time.Now().UTC()
	signedAt := time.Unix(unix, 0).UTC()
	if signedAt.Before(now.Add(-agentSignatureSkew)) || signedAt.After(now.Add(agentSignatureSkew)) {
		return fmt.Errorf("stale agent signature")
	}
	body, err := readAndRestoreBody(r)
	if err != nil {
		return err
	}
	if agent.SecretHash(stored.AuthSecret) != stored.SecretHash {
		return fmt.Errorf("invalid agent secret")
	}
	expected := agent.Signature(r.Method, r.URL.RequestURI(), ts, body, stored.AuthSecret)
	if !constantTimeEqual(expected, sig) {
		return fmt.Errorf("invalid agent signature")
	}
	return nil
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := 0; i < len(a); i++ {
		result |= a[i] ^ b[i]
	}
	return result == 0
}

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
