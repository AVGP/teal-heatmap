package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	Addr          string
	BaseURL       string
	Storage       string // "postgres" (default) or "memory"
	DatabaseURL   string
	SessionSecret []byte
	SecureCookie  bool
	IsLoopback    bool
}

// Load reads configuration from environment variables with sensible local defaults.
func Load() (*Config, error) {
	port := envOr("PORT", "8080")
	addr := envOr("LISTEN_ADDR", "127.0.0.1:"+port)

	baseURL := strings.TrimRight(envOr("BASE_URL", fmt.Sprintf("http://127.0.0.1:%s", port)), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BASE_URL %q: %w", baseURL, err)
	}

	host := parsed.Hostname()
	isLoopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	secureCookie := parsed.Scheme == "https"

	dbURL := envOr("DATABASE_URL", "postgres://postgres:postgres@127.0.0.1:5432/teal_heatmap?sslmode=disable")

	secretHex := os.Getenv("SESSION_SECRET")
	var secret []byte
	if secretHex != "" {
		secret, err = hex.DecodeString(secretHex)
		if err != nil || len(secret) < 32 {
			// Accept arbitrary passphrase string as fallback if >= 32 bytes.
			if len(secretHex) >= 32 {
				secret = []byte(secretHex)
			} else {
				return nil, fmt.Errorf("SESSION_SECRET must be at least 32 bytes (or 64 hex chars)")
			}
		}
	} else {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate ephemeral session secret: %w", err)
		}
		slog.Warn("SESSION_SECRET not set; generated ephemeral secret (browser sessions will reset on restart)")
	}

	storage := strings.ToLower(envOr("STORAGE", "postgres"))
	if storage != "postgres" && storage != "memory" {
		return nil, fmt.Errorf("invalid STORAGE %q: must be \"postgres\" or \"memory\"", storage)
	}

	return &Config{
		Addr:          addr,
		BaseURL:       baseURL,
		Storage:       storage,
		DatabaseURL:   dbURL,
		SessionSecret: secret,
		SecureCookie:  secureCookie,
		IsLoopback:    isLoopback,
	}, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
