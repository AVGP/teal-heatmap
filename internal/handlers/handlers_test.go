package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"teal-heatmap/internal/api"
	"teal-heatmap/internal/auth"
	"teal-heatmap/internal/config"
	"teal-heatmap/internal/store"
	"teal-heatmap/internal/visualization"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestEndToEnd_MemoryStoreSyncAndDashboard(t *testing.T) {
	cfg := &config.Config{
		Addr:          "127.0.0.1:8080",
		BaseURL:       "http://127.0.0.1:8080",
		Storage:       "memory",
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		IsLoopback:    true,
	}
	mem := store.NewMemoryStore()
	renderer, err := visualization.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	tealClient := api.NewTealClient()
	tealClient.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var payload map[string]any
			if r.URL.Query().Get("collection") == "fm.teal.feed.play" {
				payload = map[string]any{
					"records": []map[string]any{
						{
							"uri": "at://did:plc:alice/fm.teal.feed.play/rk1",
							"value": map[string]any{
								"trackName":   "Teal Wave",
								"artistNames": []string{"Oceanic"},
								"releaseName": "Harbor",
								"playedTime":  today + "T12:30:00Z",
							},
						},
					},
				}
			} else {
				payload = map[string]any{"records": []any{}}
			}
			b, _ := json.Marshal(payload)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(b)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}),
	}

	srv := NewServer(cfg, mem, auth.NewClient(cfg, mem), tealClient, renderer)
	h := srv.Handler()

	// 1. Health check works with in-memory store.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", rec.Code)
	}

	// 2. Login page renders without any demo/test account button.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on login page, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "demo-login") {
		t.Fatalf("did not expect demo-login endpoint on login page")
	}

	// 3. Create an authenticated session in MemoryStore and sync from mocked PDS.
	appSess, err := mem.CreateAppSession(context.Background(), "did:plc:alice", "oauth-sess-1", "alice.bsky.social", "https://pds.example", time.Hour)
	if err != nil {
		t.Fatalf("CreateAppSession: %v", err)
	}
	sessCookie := &http.Cookie{Name: sessionCookieName, Value: appSess.Token}

	syncReq := httptest.NewRequest(http.MethodPost, "/sync", nil)
	syncReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, syncReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on /sync, got %d", rec.Code)
	}

	// 4. Dashboard renders inline SVG heatmap and synced track for @alice.bsky.social.
	dashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dashReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on dashboard, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "@alice.bsky.social") || !strings.Contains(body, `class="hm-svg"`) || !strings.Contains(body, "Teal Wave") {
		t.Fatalf("expected dashboard with @alice.bsky.social, SVG heatmap, and synced track")
	}

	// 5. Day API returns synced play for today.
	dayReq := httptest.NewRequest(http.MethodGet, "/api/day?date="+today, nil)
	dayReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dayReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/day, got %d", rec.Code)
	}
	var dayResp struct {
		Date  string `json:"date"`
		Plays []any  `json:"plays"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dayResp); err != nil {
		t.Fatalf("decode /api/day response: %v", err)
	}
	if dayResp.Date != today || len(dayResp.Plays) != 1 {
		t.Fatalf("expected 1 synced play for %s, got %+v", today, dayResp)
	}

	// 6. Logout clears session.
	outReq := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	outReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, outReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on logout, got %d", rec.Code)
	}
}
