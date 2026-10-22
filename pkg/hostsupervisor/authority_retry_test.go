package hostsupervisor

import (
	"context"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type authorityTransport func(*http.Request) (*http.Response, error)

func (f authorityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthorityCheckRetriesTransportFailureWithinOriginalDeadline(t *testing.T) {
	calls := 0
	c := AuthorityClient{Identity: agent.Identity{ID: "agent", Secret: "secret", APIURL: "http://api"}, HTTP: &http.Client{Transport: authorityTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.EOF
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, c.Authorize(ctx, controlplane.SignedExecutionGrant{}))
	require.Equal(t, 2, calls)
}

func TestAuthorityCheckNeverRetriesExplicitRejection(t *testing.T) {
	for _, status := range []int{401, 403, 409, 503} {
		calls := 0
		c := AuthorityClient{Identity: agent.Identity{ID: "agent", Secret: "secret", APIURL: "http://api"}, HTTP: &http.Client{Transport: authorityTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})}}
		require.Error(t, c.Authorize(context.Background(), controlplane.SignedExecutionGrant{}))
		require.Equal(t, 1, calls)
	}
}

func TestAuthorityCheckRetryCannotExtendDeadline(t *testing.T) {
	calls := 0
	c := AuthorityClient{Identity: agent.Identity{ID: "agent", Secret: "secret", APIURL: "http://api"}, HTTP: &http.Client{Transport: authorityTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.Authorize(ctx, controlplane.SignedExecutionGrant{}), context.DeadlineExceeded)
	require.Equal(t, 1, calls, "watchdog cancellation must prevent another request")
}

func TestAuthorityCheckRetriesAreBoundedAndStopOnRejection(t *testing.T) {
	for _, reject := range []bool{false, true} {
		calls := 0
		c := AuthorityClient{Identity: agent.Identity{ID: "agent", Secret: "secret", APIURL: "http://api"}, HTTP: &http.Client{Transport: authorityTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if reject && calls == 2 {
				return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			}
			return nil, io.EOF
		})}}
		require.Error(t, c.Authorize(context.Background(), controlplane.SignedExecutionGrant{}))
		if reject {
			require.Equal(t, 2, calls)
		} else {
			require.Equal(t, 3, calls)
		}
	}
}

func TestAuthorityObsoleteRequiresExplicitConflict(t *testing.T) {
	for _, status := range []int{401, 403, 409, 500, 503} {
		c := AuthorityClient{Identity: agent.Identity{ID: "agent", Secret: "secret", APIURL: "http://api"}, HTTP: &http.Client{Transport: authorityTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})}}
		err := c.Authorize(context.Background(), controlplane.SignedExecutionGrant{})
		if status == 409 {
			require.ErrorIs(t, err, ErrGrantObsolete)
		} else {
			require.NotErrorIs(t, err, ErrGrantObsolete)
		}
	}
}
