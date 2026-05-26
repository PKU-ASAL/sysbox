package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/config"
)

func TestAuthMiddlewareAcceptsQueryTokenForWebSocketClients(t *testing.T) {
	cfg := config.ServiceConfig{}
	cfg.API.Token = "secret"
	s := NewServerWithConfig(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health?token=secret", nil)
	authMiddleware(s).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddlewareRejectsBadQueryToken(t *testing.T) {
	cfg := config.ServiceConfig{}
	cfg.API.Token = "secret"
	s := NewServerWithConfig(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health?token=wrong", nil)
	authMiddleware(s).ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
