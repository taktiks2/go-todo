package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/taktiks2/go-todo/backend/internal/http"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)

	httpapi.NewHandler().Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("レスポンスボディを JSON としてデコードできない: %v (body = %q)", err, rec.Body.String())
	}

	if len(body) != 1 || body["status"] != "ok" {
		t.Errorf("body = %v, want map[status:ok]", body)
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "GET /api/healthz は 200", method: http.MethodGet, path: "/api/healthz", wantStatus: http.StatusOK},
		{name: "POST /api/healthz は 405", method: http.MethodPost, path: "/api/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "未登録のパスは 404", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)

			httpapi.NewHandler().Routes().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
