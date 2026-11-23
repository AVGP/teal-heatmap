package store

import (
	"context"
	"time"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"

	"teal-heatmap/internal/process"
	"teal-heatmap/internal/visualization"
)

// Store defines the persistence contract implemented by both PostgresStore
// (production) and MemoryStore (testing mode).
type Store interface {
	oauth.ClientAuthStore

	Ping(ctx context.Context) error
	Close()
	CleanupExpired(ctx context.Context) error

	CreateAppSession(ctx context.Context, did, oauthSessionID, handle, hostURL string, ttl time.Duration) (*AppSession, error)
	GetAppSession(ctx context.Context, token string) (*AppSession, error)
	DeleteAppSession(ctx context.Context, token string) error

	HasPlay(ctx context.Context, did, collection, rkey string) (bool, error)
	UpsertPlays(ctx context.Context, plays []process.PlayRecord) error
	DeletePlaysForDID(ctx context.Context, did string) error
	GetDailyCounts(ctx context.Context, did string, start, end time.Time) (map[string]int, error)
	GetAvailableYears(ctx context.Context, did string) ([]int, error)
	GetPlaysForDate(ctx context.Context, did, dateStr string) ([]process.PlayRecord, error)
	GetTopArtists(ctx context.Context, did string, start, end time.Time, limit int) ([]visualization.ArtistCount, error)
	GetRecentPlays(ctx context.Context, did string, limit int) ([]process.PlayRecord, error)
	GetSyncState(ctx context.Context, did string) (*SyncState, error)
	UpdateSyncState(ctx context.Context, did string) error
}

// Compile-time interface checks.
var (
	_ Store = (*PostgresStore)(nil)
	_ Store = (*MemoryStore)(nil)
)
