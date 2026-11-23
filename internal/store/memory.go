package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"teal-heatmap/internal/process"
	"teal-heatmap/internal/visualization"
)

type storedAuthReq struct {
	data      oauth.AuthRequestData
	createdAt time.Time
}

type playKey struct {
	collection string
	rkey       string
}

// MemoryStore is a thread-safe in-memory implementation of Store that supports
// full ATProto OAuth, browser sessions, and cached teal.fm play history without PostgreSQL.
type MemoryStore struct {
	mu           sync.RWMutex
	authRequests map[string]storedAuthReq
	oauthSess    map[string]oauth.ClientSessionData // key: did + "|" + sessionID
	appSessions  map[string]AppSession              // key: token
	plays        map[string]map[playKey]process.PlayRecord
	syncStates   map[string]SyncState
}

// NewMemoryStore initializes an empty in-memory datastore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		authRequests: make(map[string]storedAuthReq),
		oauthSess:    make(map[string]oauth.ClientSessionData),
		appSessions:  make(map[string]AppSession),
		plays:        make(map[string]map[playKey]process.PlayRecord),
		syncStates:   make(map[string]SyncState),
	}
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
func (m *MemoryStore) Close()                     {}

func oauthKey(did syntax.DID, sessionID string) string {
	return did.String() + "|" + sessionID
}

func cloneSessionData(s oauth.ClientSessionData) oauth.ClientSessionData {
	cp := s
	if s.Scopes != nil {
		cp.Scopes = append([]string(nil), s.Scopes...)
	}
	return cp
}

func cloneAuthRequestData(r oauth.AuthRequestData) oauth.AuthRequestData {
	cp := r
	if r.Scopes != nil {
		cp.Scopes = append([]string(nil), r.Scopes...)
	}
	if r.AccountDID != nil {
		did := *r.AccountDID
		cp.AccountDID = &did
	}
	return cp
}

// --- oauth.ClientAuthStore ---

func (m *MemoryStore) GetSession(_ context.Context, did syntax.DID, sessionID string) (*oauth.ClientSessionData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.oauthSess[oauthKey(did, sessionID)]
	if !ok {
		return nil, fmt.Errorf("oauth session not found: %w", ErrNotFound)
	}
	cp := cloneSessionData(sess)
	return &cp, nil
}

func (m *MemoryStore) SaveSession(_ context.Context, sess oauth.ClientSessionData) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oauthSess[oauthKey(sess.AccountDID, sess.SessionID)] = cloneSessionData(sess)
	return nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, did syntax.DID, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.oauthSess, oauthKey(did, sessionID))
	return nil
}

func (m *MemoryStore) GetAuthRequestInfo(_ context.Context, state string) (*oauth.AuthRequestData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	req, ok := m.authRequests[state]
	if !ok || time.Since(req.createdAt) > 30*time.Minute {
		return nil, fmt.Errorf("auth request not found or expired: %w", ErrNotFound)
	}
	cp := cloneAuthRequestData(req.data)
	return &cp, nil
}

func (m *MemoryStore) SaveAuthRequestInfo(_ context.Context, info oauth.AuthRequestData) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.authRequests[info.State]; exists {
		return fmt.Errorf("auth request already exists for state %q", info.State)
	}
	m.authRequests[info.State] = storedAuthReq{
		data:      cloneAuthRequestData(info),
		createdAt: time.Now().UTC(),
	}
	return nil
}

func (m *MemoryStore) DeleteAuthRequestInfo(_ context.Context, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.authRequests, state)
	return nil
}

// --- Browser AppSession management ---

func (m *MemoryStore) CreateAppSession(_ context.Context, did, oauthSessionID, handle, hostURL string, ttl time.Duration) (*AppSession, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	sess := AppSession{
		Token:          token,
		DID:            did,
		OAuthSessionID: oauthSessionID,
		Handle:         handle,
		HostURL:        hostURL,
		ExpiresAt:      time.Now().UTC().Add(ttl),
	}

	m.mu.Lock()
	m.appSessions[token] = sess
	m.mu.Unlock()

	cp := sess
	return &cp, nil
}

func (m *MemoryStore) GetAppSession(_ context.Context, token string) (*AppSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.appSessions[token]
	if !ok || !sess.ExpiresAt.After(time.Now().UTC()) {
		return nil, ErrNotFound
	}
	cp := sess
	return &cp, nil
}

func (m *MemoryStore) DeleteAppSession(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.appSessions, token)
	return nil
}

func (m *MemoryStore) CleanupExpired(_ context.Context) error {
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.appSessions {
		if !s.ExpiresAt.After(now) {
			delete(m.appSessions, k)
		}
	}
	for k, r := range m.authRequests {
		if now.Sub(r.createdAt) > time.Hour {
			delete(m.authRequests, k)
		}
	}
	return nil
}

// --- Play history & aggregation queries ---

func (m *MemoryStore) HasPlay(_ context.Context, did, collection, rkey string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	userPlays := m.plays[did]
	if userPlays == nil {
		return false, nil
	}
	_, ok := userPlays[playKey{collection: collection, rkey: rkey}]
	return ok, nil
}

func (m *MemoryStore) UpsertPlays(_ context.Context, plays []process.PlayRecord) error {
	if len(plays) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range plays {
		p.PlayedAt = p.PlayedAt.UTC()
		userPlays := m.plays[p.DID]
		if userPlays == nil {
			userPlays = make(map[playKey]process.PlayRecord)
			m.plays[p.DID] = userPlays
		}
		userPlays[playKey{collection: p.Collection, rkey: p.RKey}] = p
	}
	return nil
}

func (m *MemoryStore) DeletePlaysForDID(_ context.Context, did string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.plays, did)
	return nil
}

func (m *MemoryStore) GetDailyCounts(_ context.Context, did string, start, end time.Time) (map[string]int, error) {
	startUTC := start.UTC()
	endUTC := end.UTC()

	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := make(map[string]int)
	for _, p := range m.plays[did] {
		if !p.PlayedAt.Before(startUTC) && !p.PlayedAt.After(endUTC) {
			counts[p.PlayedAt.Format("2006-01-02")]++
		}
	}
	return counts, nil
}

func (m *MemoryStore) GetAvailableYears(_ context.Context, did string) ([]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	yearSet := make(map[int]struct{})
	for _, p := range m.plays[did] {
		yearSet[p.PlayedAt.UTC().Year()] = struct{}{}
	}
	years := make([]int, 0, len(yearSet))
	for y := range yearSet {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	return years, nil
}

func (m *MemoryStore) GetPlaysForDate(_ context.Context, did, dateStr string) ([]process.PlayRecord, error) {
	dayStart, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q: %w", dateStr, err)
	}
	dayStart = dayStart.UTC()
	dayEnd := dayStart.Add(24 * time.Hour)

	m.mu.RLock()
	defer m.mu.RUnlock()
	var plays []process.PlayRecord
	for _, p := range m.plays[did] {
		if !p.PlayedAt.Before(dayStart) && p.PlayedAt.Before(dayEnd) {
			plays = append(plays, p)
		}
	}
	sort.Slice(plays, func(i, j int) bool {
		return plays[i].PlayedAt.After(plays[j].PlayedAt)
	})
	if len(plays) > 200 {
		plays = plays[:200]
	}
	return plays, nil
}

func (m *MemoryStore) GetTopArtists(_ context.Context, did string, start, end time.Time, limit int) ([]visualization.ArtistCount, error) {
	startUTC := start.UTC()
	endUTC := end.UTC()

	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := make(map[string]int)
	for _, p := range m.plays[did] {
		if p.ArtistName == "" {
			continue
		}
		if !p.PlayedAt.Before(startUTC) && !p.PlayedAt.After(endUTC) {
			counts[p.ArtistName]++
		}
	}
	artists := make([]visualization.ArtistCount, 0, len(counts))
	for name, cnt := range counts {
		artists = append(artists, visualization.ArtistCount{Name: name, Count: cnt})
	}
	sort.Slice(artists, func(i, j int) bool {
		if artists[i].Count != artists[j].Count {
			return artists[i].Count > artists[j].Count
		}
		return artists[i].Name < artists[j].Name
	})
	if limit > 0 && len(artists) > limit {
		artists = artists[:limit]
	}
	return artists, nil
}

func (m *MemoryStore) GetRecentPlays(_ context.Context, did string, limit int) ([]process.PlayRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	plays := make([]process.PlayRecord, 0, len(m.plays[did]))
	for _, p := range m.plays[did] {
		plays = append(plays, p)
	}
	sort.Slice(plays, func(i, j int) bool {
		return plays[i].PlayedAt.After(plays[j].PlayedAt)
	})
	if limit > 0 && len(plays) > limit {
		plays = plays[:limit]
	}
	return plays, nil
}

func (m *MemoryStore) GetSyncState(_ context.Context, did string) (*SyncState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.syncStates[did]
	if !ok {
		return nil, ErrNotFound
	}
	cp := st
	return &cp, nil
}

func (m *MemoryStore) UpdateSyncState(_ context.Context, did string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncStates[did] = SyncState{
		DID:        did,
		LastSynced: time.Now().UTC(),
		TotalPlays: len(m.plays[did]),
	}
	return nil
}
