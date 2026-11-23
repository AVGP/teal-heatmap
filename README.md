# teal.fm Heatmap

A fast, lightweight Go web application that lets you sign in with your Atmosphere (ATProto) account and visualize your [teal.fm](https://teal.fm) listening history (`fm.teal.feed.play` and legacy `fm.teal.alpha.feed.play` records) as a GitHub-style contribution heatmap.

## Highlights

- **Atmosphere OAuth (PKCE + DPoP + PAR)** via `github.com/bluesky-social/indigo/atproto/auth/oauth` — works out of the box in both local loopback mode (`http://127.0.0.1:8080`) and HTTPS production mode (`/oauth/client-metadata.json`).
- **Built for 3G speed & modern web guidance**:
  - Zero external fonts, zero CDN assets, zero frontend JS framework.
  - Server-side rendered HTML + inline accessible SVG heatmap.
  - Gzip compression + weak ETag (`304 Not Modified`) — full dashboard payload fits under **14 KB gzipped** (single TCP round-trip).
  - Progressive enhancement: works without JS, with ~1.5 KB inline vanilla JS for zero-latency tooltips, keyboard navigation, and click-to-inspect day tracks.
  - Strict security headers (CSP, `HttpOnly` + `SameSite=Lax` cookies, `X-Frame-Options: DENY`).
- **teal.fm color scheme**: Deep slate-teal dark mode by default, plus automatic `prefers-color-scheme: light` support and a manual theme toggle.
- **PostgreSQL caching (`pgx/v5`)**: Persists OAuth sessions, browser sessions, and synced play records with incremental sync so repeat loads are sub-10ms.

## Quick Start

### Option A: In-Memory Datastore (No PostgreSQL Required)

Run with `-storage memory` (or `STORAGE=memory`):

```bash
go run ./cmd/server -storage memory
```

Then open **http://127.0.0.1:8080** and sign in with your Atmosphere handle (e.g. `alice.bsky.social`). Full ATProto OAuth and `teal.fm` PDS syncing work normally, with sessions and cached plays kept in memory until restart.

### Option B: Docker Compose (App + PostgreSQL)

Build and start both PostgreSQL and the main application:

```bash
docker compose up -d --build
```

Then open **http://127.0.0.1:8080** in your browser (use `127.0.0.1`, not `localhost`, to match ATProto OAuth loopback rules) and sign in with your Atmosphere handle (e.g. `alice.bsky.social`).

To run only PostgreSQL in Docker while running the Go server locally:

```bash
docker compose up -d postgres
go run ./cmd/server
```

### 3. Production Deployment (HTTPS)

Set `BASE_URL=https://your-domain.example` and a persistent `SESSION_SECRET` (e.g. in `.env`) and deploy with `docker compose up -d --build` behind your TLS reverse proxy (or run the binary directly with `LISTEN_ADDR=:8080`). The server automatically switches from loopback OAuth to public client metadata mode and serves `/oauth/client-metadata.json`.

## Project Structure

- `cmd/server/main.go` — Entrypoint, graceful shutdown, and background session cleanup.
- `internal/config` — Environment configuration.
- `internal/auth` — ATProto OAuth client setup & metadata document.
- `internal/store` — PostgreSQL pool, migrations, `oauth.ClientAuthStore`, and play history queries.
- `internal/api` — ATProto PDS `com.atproto.repo.listRecords` client for `fm.teal.feed.play` & `fm.teal.alpha.feed.play`.
- `internal/process` — Calendar grid builder, quartile intensity levels, and streak statistics.
- `internal/visualization` — Inline SVG heatmap renderer and embedded HTML templates.
- `internal/handlers` — HTTP routes, gzip/ETag/security middleware, and sync endpoints.
