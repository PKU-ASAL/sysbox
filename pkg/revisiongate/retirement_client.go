package revisiongate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func PostRetirement(ctx context.Context, endpoint, tokenFile string, payload any) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return fmt.Errorf("federated endpoint requires HTTPS or loopback")
	}
	key, err := privateToken(tokenFile)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(endpoint, "/")+"/v1/revisions/federated-retire", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("federation redirect forbidden") }}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("federated retirement rejected (%d)", response.StatusCode)
	}
	var result struct {
		Retired []string `json:"retired"`
		Request string   `json:"request"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 32768)).Decode(&result); err != nil {
		return err
	}
	var expected struct {
		Revisions []string `json:"revisions"`
		Request   string   `json:"request"`
	}
	if err = json.Unmarshal(raw, &expected); err != nil {
		return err
	}
	a, _ := json.Marshal(result.Retired)
	b, _ := json.Marshal(expected.Revisions)
	if result.Request != expected.Request || string(a) != string(b) {
		return fmt.Errorf("federation acknowledgement mismatch")
	}
	return nil
}
