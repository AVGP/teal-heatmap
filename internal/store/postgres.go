package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"teal-heatmap/internal/process"
	"teal-heatmap/internal/visualization"
)

// ErrNotFound is returned when a requested record or session does not exist.
var ErrNotFound = errors.New("not found")

// AppSession links an HTTP session cookie to an authenticated Atmosphere account.
type AppSession struct {
	Token          string
	DID            string
	OAuthSessionID string
	Handle         string
	HostURL        string
	ExpiresAt      time.Time
}

// SyncState tracks when a user's teal.fm records were last synced from their PDS.
type SyncState struct {
	DID        string
	LastSynced time.Time
	TotalPlays int
}

// PostgresStore backs OAuth state, browser sessions, and cached teal.fm plays in PostgreSQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time check that PostgresStore implements indigo's oauth.ClientAuthStore.
var _ oauth.ClientAuthStore = (*PostgresStore)(nil)

// NewPostgresStore connects to PostgreSQL, verifies connectivity, and runs migrations.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 15
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	s := &PostgresStore{pool: pool}
	if err := s.Migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}
	return s, nil
}

// Close releases all pooled database connections.
func (s *PostgresStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Ping checks database health.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Migrate creates all required tables and indexes if they do not already exist.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS oauth_auth_requests (
	state TEXT PRIMARY KEY,
	data JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS oauth_sessions (
	did TEXT NOT NULL,
	session_id TEXT NOT NULL,
	data JSONB NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (did, session_id)
);

CREATE TABLE IF NOT EXISTS app_sessions (
	token TEXT PRIMARY KEY,
	did TEXT NOT NULL,
	oauth_session_id TEXT NOT NULL,
	handle TEXT NOT NULL,
	host_url TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_app_sessions_expires_at ON app_sessions (expires_at);

CREATE TABLE IF NOT EXISTS plays (
	did TEXT NOT NULL,
	collection TEXT NOT NULL,
	rkey TEXT NOT NULL,
	track_name TEXT NOT NULL,
	artist_name TEXT NOT NULL,
	release_name TEXT NOT NULL DEFAULT '',
	played_at TIMESTAMPTZ NOT NULL,
	origin_url TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (did, collection, rkey)
);

CREATE INDEX IF NOT EXISTS idx_plays_did_played_at ON plays (did, played_at DESC);

CREATE TABLE IF NOT EXISTS sync_state (
	did TEXT PRIMARY KEY,
	last_synced TIMESTAMPTZ NOT NULL,
	total_plays INT NOT NULL DEFAULT 0
);
`
	_, err := s.pool.Exec(ctx, ddl)
	return err
}

// --- oauth.ClientAuthStore implementation ---

func (s *PostgresStore) GetSession(ctx context.Context, did syntax.DID, sessionID string) (*oauth.ClientSessionData, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM oauth_sessions WHERE did = $1 AND session_id = $2`,
		did.String(), sessionID,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("oauth session not found: %w", ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var sess oauth.ClientSessionData
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal oauth session: %w", err)
	}
	return &sess, nil
}

func (s *PostgresStore) SaveSession(ctx context.Context, sess oauth.ClientSessionData) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal oauth session: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO oauth_sessions (did, session_id, data, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (did, session_id)
		DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()
	`, sess.AccountDID.String(), sess.SessionID, raw)
	return err
}

func (s *PostgresStore) DeleteSession(ctx context.Context, did syntax.DID, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM oauth_sessions WHERE did = $1 AND session_id = $2`,
		did.String(), sessionID,
	)
	return err
}

func (s *PostgresStore) GetAuthRequestInfo(ctx context.Context, state string) (*oauth.AuthRequestData, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM oauth_auth_requests WHERE state = $1 AND created_at > NOW() - INTERVAL '30 minutes'`,
		state,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("auth request not found or expired: %w", ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var info oauth.AuthRequestData
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("unmarshal auth request: %w", err)
	}
	return &info, nil
}

func (s *PostgresStore) SaveAuthRequestInfo(ctx context.Context, info oauth.AuthRequestData) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal auth request: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO oauth_auth_requests (state, data, created_at) VALUES ($1, $2, NOW())`,
		info.State, raw,
	)
	return err
}

func (s *PostgresStore) DeleteAuthRequestInfo(ctx context.Context, state string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM oauth_auth_requests WHERE state = $1`, state)
	return err
}

// --- Browser AppSession management ---

func (s *PostgresStore) CreateAppSession(ctx context.Context, did, oauthSessionID, handle, hostURL string, ttl time.Duration) (*AppSession, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expiresAt := time.Now().UTC().Add(ttl)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_sessions (token, did, oauth_session_id, handle, host_url, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, token, did, oauthSessionID, handle, hostURL, expiresAt)
	if err != nil {
		return nil, err
	}
	return &AppSession{
		Token:          token,
		DID:            did,
		OAuthSessionID: oauthSessionID,
		Handle:         handle,
		HostURL:        hostURL,
		ExpiresAt:      expiresAt,
	}, nil
}

func (s *PostgresStore) GetAppSession(ctx context.Context, token string) (*AppSession, error) {
	var sess AppSession
	err := s.pool.QueryRow(ctx, `
		SELECT token, did, oauth_session_id, handle, host_url, expires_at
		FROM app_sessions
		WHERE token = $1 AND expires_at > NOW()
	`, token).Scan(&sess.Token, &sess.DID, &sess.OAuthSessionID, &sess.Handle, &sess.HostURL, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *PostgresStore) DeleteAppSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM app_sessions WHERE token = $1`, token)
	return err
}

// CleanupExpired removes expired browser sessions and stale OAuth PAR requests.
func (s *PostgresStore) CleanupExpired(ctx context.Context) error {
	_, err1 := s.pool.Exec(ctx, `DELETE FROM app_sessions WHERE expires_at <= NOW()`)
	_, err2 := s.pool.Exec(ctx, `DELETE FROM oauth_auth_requests WHERE created_at <= NOW() - INTERVAL '1 hour'`)
	return errors.Join(err1, err2)
}

// --- Play history & aggregation queries ---

// HasPlay returns true if a specific (did, collection, rkey) play is already cached.
func (s *PostgresStore) HasPlay(ctx context.Context, did, collection, rkey string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM plays WHERE did = $1 AND collection = $2 AND rkey = $3)`,
		did, collection, rkey,
	).Scan(&exists)
	return exists, err
}

// UpsertPlays inserts or updates a batch of play records in a single pipelined batch.
func (s *PostgresStore) UpsertPlays(ctx context.Context, plays []process.PlayRecord) error {
	if len(plays) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	const q = `
		INSERT INTO plays (did, collection, rkey, track_name, artist_name, release_name, played_at, origin_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (did, collection, rkey) DO UPDATE SET
			track_name = EXCLUDED.track_name,
			artist_name = EXCLUDED.artist_name,
			release_name = EXCLUDED.release_name,
			played_at = EXCLUDED.played_at,
			origin_url = EXCLUDED.origin_url
	`
	for _, p := range plays {
		batch.Queue(q, p.DID, p.Collection, p.RKey, p.TrackName, p.ArtistName, p.ReleaseName, p.PlayedAt.UTC(), p.OriginURL)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range plays {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// DeletePlaysForDID clears cached plays for a user prior to a full resync.
func (s *PostgresStore) DeletePlaysForDID(ctx context.Context, did string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM plays WHERE did = $1`, did)
	return err
}

// GetDailyCounts returns a map of "YYYY-MM-DD" (UTC) -> play count for the given date window.
func (s *PostgresStore) GetDailyCounts(ctx context.Context, did string, start, end time.Time) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT TO_CHAR(played_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, COUNT(*)::INT
		FROM plays
		WHERE did = $1 AND played_at >= $2 AND played_at <= $3
		GROUP BY day
	`, did, start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int, 366)
	for rows.Next() {
		var day string
		var cnt int
		if err := rows.Scan(&day, &cnt); err != nil {
			return nil, err
		}
		counts[day] = cnt
	}
	return counts, rows.Err()
}

// GetAvailableYears returns all distinct calendar years that have plays for the user.
func (s *PostgresStore) GetAvailableYears(ctx context.Context, did string) ([]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT EXTRACT(YEAR FROM played_at AT TIME ZONE 'UTC')::INT AS yr
		FROM plays
		WHERE did = $1
		ORDER BY yr DESC
	`, did)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var years []int
	for rows.Next() {
		var yr int
		if err := rows.Scan(&yr); err != nil {
			return nil, err
		}
		years = append(years, yr)
	}
	return years, rows.Err()
}

// GetPlaysForDate returns all plays for a user on a specific UTC calendar date (YYYY-MM-DD).
func (s *PostgresStore) GetPlaysForDate(ctx context.Context, did, dateStr string) ([]process.PlayRecord, error) {
	dayStart, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q: %w", dateStr, err)
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	rows, err := s.pool.Query(ctx, `
		SELECT did, collection, rkey, track_name, artist_name, release_name, played_at, origin_url
		FROM plays
		WHERE did = $1 AND played_at >= $2 AND played_at < $3
		ORDER BY played_at DESC
		LIMIT 200
	`, did, dayStart.UTC(), dayEnd.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plays []process.PlayRecord
	for rows.Next() {
		var p process.PlayRecord
		if err := rows.Scan(&p.DID, &p.Collection, &p.RKey, &p.TrackName, &p.ArtistName, &p.ReleaseName, &p.PlayedAt, &p.OriginURL); err != nil {
			return nil, err
		}
		plays = append(plays, p)
	}
	return plays, rows.Err()
}

// GetTopArtists returns the top N artists for a user within [start, end].
func (s *PostgresStore) GetTopArtists(ctx context.Context, did string, start, end time.Time, limit int) ([]visualization.ArtistCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT artist_name, COUNT(*)::INT AS cnt
		FROM plays
		WHERE did = $1 AND played_at >= $2 AND played_at <= $3 AND artist_name <> ''
		GROUP BY artist_name
		ORDER BY cnt DESC, artist_name ASC
		LIMIT $4
	`, did, start.UTC(), end.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artists []visualization.ArtistCount
	for rows.Next() {
		var a visualization.ArtistCount
		if err := rows.Scan(&a.Name, &a.Count); err != nil {
			return nil, err
		}
		artists = append(artists, a)
	}
	return artists, rows.Err()
}

// GetRecentPlays returns the most recent N plays for a user.
func (s *PostgresStore) GetRecentPlays(ctx context.Context, did string, limit int) ([]process.PlayRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT did, collection, rkey, track_name, artist_name, release_name, played_at, origin_url
		FROM plays
		WHERE did = $1
		ORDER BY played_at DESC
		LIMIT $2
	`, did, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plays []process.PlayRecord
	for rows.Next() {
		var p process.PlayRecord
		if err := rows.Scan(&p.DID, &p.Collection, &p.RKey, &p.TrackName, &p.ArtistName, &p.ReleaseName, &p.PlayedAt, &p.OriginURL); err != nil {
			return nil, err
		}
		plays = append(plays, p)
	}
	return plays, rows.Err()
}

// GetSyncState fetches the last sync metadata for a user.
func (s *PostgresStore) GetSyncState(ctx context.Context, did string) (*SyncState, error) {
	var st SyncState
	err := s.pool.QueryRow(ctx,
		`SELECT did, last_synced, total_plays FROM sync_state WHERE did = $1`,
		did,
	).Scan(&st.DID, &st.LastSynced, &st.TotalPlays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpdateSyncState records the current sync timestamp and total cached play count.
func (s *PostgresStore) UpdateSyncState(ctx context.Context, did string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sync_state (did, last_synced, total_plays)
		VALUES ($1, NOW(), (SELECT COUNT(*)::INT FROM plays WHERE did = $1))
		ON CONFLICT (did) DO UPDATE SET
			last_synced = NOW(),
			total_plays = (SELECT COUNT(*)::INT FROM plays WHERE did = $1)
	`, did)
	return err
}
