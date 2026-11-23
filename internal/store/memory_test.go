package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"teal-heatmap/internal/process"
)

func TestMemoryStore_OAuthAndAppSessions(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()

	if err := m.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// OAuth auth request lifecycle (including create-only enforcement).
	if err := m.SaveAuthRequestInfo(ctx, oauth.AuthRequestData{State: "st-1", AuthServerURL: "https://bsky.social"}); err != nil {
		t.Fatalf("SaveAuthRequestInfo: %v", err)
	}
	if err := m.SaveAuthRequestInfo(ctx, oauth.AuthRequestData{State: "st-1", AuthServerURL: "https://other.example"}); err == nil {
		t.Fatalf("expected duplicate SaveAuthRequestInfo to fail")
	}
	info, err := m.GetAuthRequestInfo(ctx, "st-1")
	if err != nil || info.AuthServerURL != "https://bsky.social" {
		t.Fatalf("GetAuthRequestInfo unexpected: %+v, err=%v", info, err)
	}
	_ = m.DeleteAuthRequestInfo(ctx, "st-1")
	if _, err := m.GetAuthRequestInfo(ctx, "st-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// OAuth session lifecycle (upsert + deep copy).
	did := syntax.DID("did:plc:memtest")
	if err := m.SaveSession(ctx, oauth.ClientSessionData{AccountDID: did, SessionID: "s1", HostURL: "https://pds.example", Scopes: []string{"atproto"}}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	sess, err := m.GetSession(ctx, did, "s1")
	if err != nil || sess.HostURL != "https://pds.example" || len(sess.Scopes) != 1 {
		t.Fatalf("GetSession unexpected: %+v, err=%v", sess, err)
	}
	sess.Scopes[0] = "mutated"
	sessAgain, _ := m.GetSession(ctx, did, "s1")
	if sessAgain.Scopes[0] != "atproto" {
		t.Fatalf("expected stored Scopes to be isolated from caller mutation")
	}

	// Browser app session lifecycle.
	appSess, err := m.CreateAppSession(ctx, did.String(), "s1", "alice.bsky.social", "https://pds.example", time.Hour)
	if err != nil {
		t.Fatalf("CreateAppSession: %v", err)
	}
	got, err := m.GetAppSession(ctx, appSess.Token)
	if err != nil || got.Handle != "alice.bsky.social" {
		t.Fatalf("GetAppSession unexpected: %+v, err=%v", got, err)
	}
	_ = m.DeleteAppSession(ctx, appSess.Token)
	if _, err := m.GetAppSession(ctx, appSess.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after DeleteAppSession, got %v", err)
	}
}

func TestMemoryStore_PlaysQueriesAndDeduplication(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	did := "did:plc:listener1"
	now := time.Date(2026, time.November, 22, 18, 0, 0, 0, time.UTC)

	records := []process.PlayRecord{
		{DID: did, Collection: "fm.teal.feed.play", RKey: "rk1", TrackName: "Track 1", ArtistName: "Artist A", ReleaseName: "Album 1", PlayedAt: now},
		{DID: did, Collection: "fm.teal.feed.play", RKey: "rk2", TrackName: "Track 2", ArtistName: "Artist A", ReleaseName: "Album 1", PlayedAt: now.Add(-10 * time.Minute)},
		{DID: did, Collection: "fm.teal.alpha.feed.play", RKey: "rk3", TrackName: "Old Track", ArtistName: "Artist B", PlayedAt: time.Date(2025, time.June, 10, 12, 0, 0, 0, time.UTC)},
	}
	if err := m.UpsertPlays(ctx, records); err != nil {
		t.Fatalf("UpsertPlays: %v", err)
	}
	if err := m.UpdateSyncState(ctx, did); err != nil {
		t.Fatalf("UpdateSyncState: %v", err)
	}

	st, err := m.GetSyncState(ctx, did)
	if err != nil || st.TotalPlays != 3 {
		t.Fatalf("expected 3 total plays in sync state, got %+v, err=%v", st, err)
	}

	years, err := m.GetAvailableYears(ctx, did)
	if err != nil || len(years) != 2 || years[0] != 2026 || years[1] != 2025 {
		t.Fatalf("expected years [2026 2025], got %v, err=%v", years, err)
	}

	dayPlays, err := m.GetPlaysForDate(ctx, did, "2026-11-22")
	if err != nil || len(dayPlays) != 2 {
		t.Fatalf("expected 2 plays on 2026-11-22, got %d, err=%v", len(dayPlays), err)
	}

	counts, err := m.GetDailyCounts(ctx, did, now.AddDate(-1, 0, 0), now.Add(24*time.Hour))
	if err != nil || counts["2026-11-22"] != 2 {
		t.Fatalf("expected count 2 on 2026-11-22, got %v, err=%v", counts, err)
	}

	artists, err := m.GetTopArtists(ctx, did, now.AddDate(-2, 0, 0), now.Add(24*time.Hour), 5)
	if err != nil || len(artists) != 2 || artists[0].Name != "Artist A" || artists[0].Count != 2 {
		t.Fatalf("unexpected top artists: %+v, err=%v", artists, err)
	}

	// Verify UpsertPlays deduplicates on (did, collection, rkey).
	updated := records[0]
	updated.TrackName = "Track 1 (Remastered)"
	_ = m.UpsertPlays(ctx, []process.PlayRecord{updated})
	recent, err := m.GetRecentPlays(ctx, did, 1)
	if err != nil || len(recent) != 1 || recent[0].TrackName != "Track 1 (Remastered)" {
		t.Fatalf("expected updated play, got %+v, err=%v", recent, err)
	}
}
