package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"

	"teal-heatmap/internal/api"
	"teal-heatmap/internal/auth"
	"teal-heatmap/internal/config"
	"teal-heatmap/internal/process"
	"teal-heatmap/internal/store"
	"teal-heatmap/internal/visualization"
)

const (
	sessionCookieName = "teal_session"
	flashCookieName   = "teal_flash"
	sessionTTL        = 30 * 24 * time.Hour
)

type syncJob struct {
	fetched atomic.Int64
	done    chan struct{}
	err     error
}

// Server holds all dependencies for HTTP request handling.
type Server struct {
	cfg      *config.Config
	store    store.Store
	auth     *auth.Client
	teal     *api.TealClient
	renderer *visualization.Renderer

	syncMu   sync.Mutex
	syncJobs map[string]*syncJob
}

// NewServer constructs a configured HTTP handler server.
func NewServer(
	cfg *config.Config,
	st store.Store,
	authClient *auth.Client,
	tealClient *api.TealClient,
	renderer *visualization.Renderer,
) *Server {
	return &Server{
		cfg:      cfg,
		store:    st,
		auth:     authClient,
		teal:     tealClient,
		renderer: renderer,
		syncJobs: make(map[string]*syncJob),
	}
}

// Handler returns the root http.Handler with routes and middleware attached.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /oauth/client-metadata.json", s.handleClientMetadata)
	mux.HandleFunc("POST /oauth/login", s.handleOAuthLogin)
	mux.HandleFunc("GET /oauth/callback", s.handleOAuthCallback)
	mux.HandleFunc("POST /oauth/logout", s.handleOAuthLogout)
	mux.HandleFunc("POST /sync", s.handleSync)
	mux.HandleFunc("GET /api/day", s.handleAPIDay)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	return SecurityHeadersMiddleware(GzipMiddleware(mux))
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	flashErr, flashInfo := s.popFlash(w, r)

	sess, err := s.currentSession(ctx, r)
	if err != nil {
		page := visualization.PageData{
			Title:      "Sign in",
			Storage:    s.cfg.Storage,
			FlashError: flashErr,
			FlashInfo:  flashInfo,
		}
		body, rerr := s.renderer.RenderToBytes(page)
		if rerr != nil {
			slog.Error("render login page", "err", rerr)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		cc := "public, max-age=60, must-revalidate"
		if flashErr != "" || flashInfo != "" {
			cc = "no-store"
		}
		writeHTMLWithETag(w, r, body, cc)
		return
	}

	selectedYear := 0
	if yStr := r.URL.Query().Get("year"); yStr != "" {
		if y, err := strconv.Atoi(yStr); err == nil && y >= 2000 && y <= 2100 {
			selectedYear = y
		}
	}

	now := time.Now().UTC()
	var rangeStart, rangeEnd time.Time
	if selectedYear > 0 {
		rangeStart = time.Date(selectedYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		rangeEnd = time.Date(selectedYear, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	} else {
		todayEnd := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, time.UTC)
		rangeEnd = todayEnd
		rangeStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -365)
	}

	counts, err := s.store.GetDailyCounts(ctx, sess.DID, rangeStart, rangeEnd)
	if err != nil {
		slog.Error("get daily counts", "did", sess.DID, "err", err)
		http.Error(w, "failed to load listening history", http.StatusInternalServerError)
		return
	}

	years, err := s.store.GetAvailableYears(ctx, sess.DID)
	if err != nil {
		slog.Warn("get available years", "did", sess.DID, "err", err)
	}

	topArtists, err := s.store.GetTopArtists(ctx, sess.DID, rangeStart, rangeEnd, 8)
	if err != nil {
		slog.Warn("get top artists", "did", sess.DID, "err", err)
	}

	recentPlays, err := s.store.GetRecentPlays(ctx, sess.DID, 8)
	if err != nil {
		slog.Warn("get recent plays", "did", sess.DID, "err", err)
	}

	lastSynced := ""
	if st, err := s.store.GetSyncState(ctx, sess.DID); err == nil {
		lastSynced = fmt.Sprintf("%s (%d total cached plays)", st.LastSynced.UTC().Format("Jan 2, 2006 15:04 UTC"), st.TotalPlays)
	}

	syncStatus := ""
	if fetched, active := s.activeSyncProgress(sess.DID); active {
		syncStatus = fmt.Sprintf("Syncing full teal.fm history in the background — %d plays fetched so far…", fetched)
	}

	heatmap := process.BuildHeatmap(counts, selectedYear, years, now, time.UTC)
	svg := visualization.RenderHeatmapSVG(heatmap)

	page := visualization.PageData{
		Title:   "@" + sess.Handle,
		Storage: s.cfg.Storage,
		User: &visualization.UserView{
			DID:    sess.DID,
			Handle: sess.Handle,
		},
		FlashError:  flashErr,
		FlashInfo:   flashInfo,
		SyncStatus:  syncStatus,
		LastSynced:  lastSynced,
		Heatmap:     heatmap,
		HeatmapSVG:  svg,
		TopArtists:  topArtists,
		RecentPlays: recentPlays,
	}

	body, err := s.renderer.RenderToBytes(page)
	if err != nil {
		slog.Error("render dashboard", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	cc := "private, no-cache"
	if flashErr != "" || flashInfo != "" || syncStatus != "" {
		cc = "no-store"
	}
	writeHTMLWithETag(w, r, body, cc)
}

func (s *Server) handleClientMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(s.auth.ClientMetadata())
}

func (s *Server) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.setFlash(w, "err", "Invalid form submission.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	handle := strings.TrimPrefix(strings.TrimSpace(r.FormValue("handle")), "@")
	if handle == "" {
		s.setFlash(w, "err", "Please enter your Atmosphere handle (e.g. alice.bsky.social).")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	redirectURL, err := s.auth.App.StartAuthFlow(r.Context(), handle)
	if err != nil {
		slog.Warn("start oauth flow failed", "handle", handle, "err", err)
		s.setFlash(w, "err", "Could not start Atmosphere sign-in: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessData, err := s.auth.App.ProcessCallback(ctx, r.URL.Query())
	if err != nil {
		slog.Warn("oauth callback failed", "err", err)
		s.setFlash(w, "err", "Atmosphere sign-in failed: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	handle := s.auth.ResolveHandle(ctx, sessData.AccountDID)
	appSess, err := s.store.CreateAppSession(
		ctx,
		sessData.AccountDID.String(),
		sessData.SessionID,
		handle,
		sessData.HostURL,
		sessionTTL,
	)
	if err != nil {
		slog.Error("create app session failed", "did", sessData.AccountDID, "err", err)
		s.setFlash(w, "err", "Failed to save session.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    appSess.Token,
		Path:     "/",
		Expires:  appSess.ExpiresAt,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})

	// Start syncing in the background and wait briefly so small histories finish
	// immediately while large histories (25k+ plays) continue without timing out.
	job, _ := s.startSync(appSess, false)
	select {
	case <-job.done:
		if job.err != nil {
			slog.Warn("initial sync failed", "did", appSess.DID, "err", job.err)
			s.setFlash(w, "info", "Signed in! Initial sync failed — click Sync now to retry.")
		} else if n := job.fetched.Load(); n > 0 {
			s.setFlash(w, "info", fmt.Sprintf("Signed in and synced %d teal.fm plays.", n))
		}
	case <-time.After(4 * time.Second):
		// Still running in background; dashboard will display live progress banner.
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleOAuthLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if sess, err := s.currentSession(ctx, r); err == nil {
		if sess.OAuthSessionID != "" && s.auth != nil {
			if did, parseErr := syntax.ParseDID(sess.DID); parseErr == nil {
				if err := s.auth.App.Logout(ctx, did, sess.OAuthSessionID); err != nil {
					slog.Warn("oauth logout warning", "did", sess.DID, "err", err)
				}
			}
		}
		_ = s.store.DeleteAppSession(ctx, sess.Token)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.currentSession(ctx, r)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	full := r.URL.Query().Get("full") == "1"
	job, started := s.startSync(sess, full)
	if !started {
		s.setFlash(w, "info", "A sync is already running in the background — refresh to see progress.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	select {
	case <-job.done:
		n := job.fetched.Load()
		if job.err != nil {
			slog.Warn("sync failed", "did", sess.DID, "full", full, "err", job.err)
			s.setFlash(w, "err", "Sync failed: "+job.err.Error())
		} else if full {
			s.setFlash(w, "info", fmt.Sprintf("Full resync complete — loaded %d teal.fm plays.", n))
		} else if n == 0 {
			s.setFlash(w, "info", "Already up to date — no new teal.fm plays found.")
		} else {
			s.setFlash(w, "info", fmt.Sprintf("Synced %d new teal.fm plays.", n))
		}
	case <-time.After(4 * time.Second):
		// Large history still syncing in background; dashboard shows live progress.
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleAPIDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.currentSession(ctx, r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	dateStr := strings.TrimSpace(r.URL.Query().Get("date"))
	if _, err := time.Parse("2006-01-02", dateStr); err != nil {
		http.Error(w, "invalid date parameter (expected YYYY-MM-DD)", http.StatusBadRequest)
		return
	}

	plays, err := s.store.GetPlaysForDate(ctx, sess.DID, dateStr)
	if err != nil {
		slog.Error("get plays for date", "did", sess.DID, "date", dateStr, "err", err)
		http.Error(w, "failed to load plays", http.StatusInternalServerError)
		return
	}
	if plays == nil {
		plays = []process.PlayRecord{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"date":  dateStr,
		"plays": plays,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, `{"status":"unhealthy"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) activeSyncProgress(did string) (int64, bool) {
	s.syncMu.Lock()
	job, ok := s.syncJobs[did]
	s.syncMu.Unlock()
	if !ok {
		return 0, false
	}
	return job.fetched.Load(), true
}

// startSync launches a background sync for sess.DID if one is not already active.
// Returns the syncJob and whether this call started a new job.
func (s *Server) startSync(sess *store.AppSession, full bool) (*syncJob, bool) {
	s.syncMu.Lock()
	if existing, ok := s.syncJobs[sess.DID]; ok {
		s.syncMu.Unlock()
		return existing, false
	}
	job := &syncJob{done: make(chan struct{})}
	s.syncJobs[sess.DID] = job
	s.syncMu.Unlock()

	did := sess.DID
	hostURL := sess.HostURL

	go func() {
		defer func() {
			s.syncMu.Lock()
			delete(s.syncJobs, did)
			s.syncMu.Unlock()
			close(job.done)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		var hasExisting func(collection, rkey string) (bool, error)
		if full {
			if err := s.store.DeletePlaysForDID(ctx, did); err != nil {
				job.err = fmt.Errorf("clear cached plays: %w", err)
				return
			}
		} else {
			hasExisting = func(collection, rkey string) (bool, error) {
				return s.store.HasPlay(ctx, did, collection, rkey)
			}
		}

		pages := 0
		err := s.teal.FetchPlaysStream(ctx, hostURL, did, hasExisting, func(batch []process.PlayRecord) error {
			if err := s.store.UpsertPlays(ctx, batch); err != nil {
				return fmt.Errorf("store plays: %w", err)
			}
			job.fetched.Add(int64(len(batch)))
			pages++
			if pages%10 == 0 {
				_ = s.store.UpdateSyncState(ctx, did)
			}
			return nil
		})
		if err != nil {
			job.err = err
			slog.Warn("background sync error", "did", did, "fetched", job.fetched.Load(), "err", err)
		}
		if err := s.store.UpdateSyncState(ctx, did); err != nil {
			slog.Warn("update sync state", "did", did, "err", err)
		}
	}()

	return job, true
}

func (s *Server) currentSession(ctx context.Context, r *http.Request) (*store.AppSession, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, store.ErrNotFound
	}
	sess, err := s.store.GetAppSession(ctx, c.Value)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("lookup app session", "err", err)
		}
		return nil, err
	}
	return sess, nil
}

func (s *Server) setFlash(w http.ResponseWriter, kind, msg string) {
	val := url.QueryEscape(kind + ":" + msg)
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    val,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) (errMsg, infoMsg string) {
	c, err := r.Cookie(flashCookieName)
	if err != nil || c.Value == "" {
		return "", ""
	}
	// Clear immediately after reading.
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	decoded, err := url.QueryUnescape(c.Value)
	if err != nil {
		return "", ""
	}
	if strings.HasPrefix(decoded, "err:") {
		return strings.TrimPrefix(decoded, "err:"), ""
	}
	if strings.HasPrefix(decoded, "info:") {
		return "", strings.TrimPrefix(decoded, "info:")
	}
	return "", ""
}
