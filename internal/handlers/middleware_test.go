package handlers

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipAndETag(t *testing.T) {
	h := SecurityHeadersMiddleware(GzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHTMLWithETag(w, r, []byte("<html><body>hello teal.fm</body></html>"), "private, no-cache")
	})))

	// First request with Accept-Encoding: gzip.
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}
	if rec1.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding, got %q", rec1.Header().Get("Content-Encoding"))
	}
	csp := rec1.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatalf("expected CSP header to be set")
	}
	if !strings.Contains(csp, "form-action 'self' https:") {
		t.Fatalf("expected CSP to allow https: OAuth redirects in form-action, got %q", csp)
	}
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected ETag header")
	}

	gr, err := gzip.NewReader(rec1.Body)
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
	}
	raw, _ := io.ReadAll(gr)
	_ = gr.Close()
	if string(raw) != "<html><body>hello teal.fm</body></html>" {
		t.Fatalf("unexpected decoded body: %q", string(raw))
	}

	// Second request with matching If-None-Match should return 304 Not Modified.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("expected empty body on 304, got %d bytes", rec2.Body.Len())
	}
}
