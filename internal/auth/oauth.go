package auth

import (
	"context"
	"strings"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"teal-heatmap/internal/config"
)

// Client wraps indigo's ATProto OAuth ClientApp and exposes metadata helpers.
type Client struct {
	App     *oauth.ClientApp
	BaseURL string
}

// NewClient initializes an ATProto OAuth client backed by the given ClientAuthStore.
func NewClient(cfg *config.Config, store oauth.ClientAuthStore) *Client {
	callbackURL := cfg.BaseURL + "/oauth/callback"
	scopes := []string{"atproto"}

	var oauthCfg oauth.ClientConfig
	if cfg.IsLoopback {
		// ATProto OAuth loopback spec requires 127.0.0.1 in redirect_uri.
		loopbackCallback := strings.Replace(callbackURL, "://localhost", "://127.0.0.1", 1)
		oauthCfg = oauth.NewLocalhostConfig(loopbackCallback, scopes)
	} else {
		clientID := cfg.BaseURL + "/oauth/client-metadata.json"
		oauthCfg = oauth.NewPublicConfig(clientID, callbackURL, scopes)
	}
	oauthCfg.UserAgent = "teal-heatmap/1.0"

	app := oauth.NewClientApp(&oauthCfg, store)
	return &Client{
		App:     app,
		BaseURL: cfg.BaseURL,
	}
}

// ClientMetadata returns the ATProto OAuth client metadata document served at
// /oauth/client-metadata.json for public HTTPS deployments.
func (c *Client) ClientMetadata() oauth.ClientMetadata {
	meta := c.App.Config.ClientMetadata()
	name := "teal.fm Heatmap"
	uri := c.BaseURL
	meta.ClientName = &name
	meta.ClientURI = &uri
	return meta
}

// ResolveHandle looks up the human-readable Atmosphere handle for a DID.
func (c *Client) ResolveHandle(ctx context.Context, did syntax.DID) string {
	ident, err := c.App.Dir.LookupDID(ctx, did)
	if err != nil || ident == nil || ident.Handle == "" {
		return did.String()
	}
	return ident.Handle.String()
}
