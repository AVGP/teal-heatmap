package visualization

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"

	"teal-heatmap/internal/process"
)

//go:embed templates/*.html
var templatesFS embed.FS

// UserView holds display information about the signed-in Atmosphere account
// or imported Spotify session.
type UserView struct {
	DID       string
	Handle    string
	IsSpotify bool
}

// ArtistCount holds an artist name and play count for the top artists list.
type ArtistCount struct {
	Name  string
	Count int
}

// PageData is the view model passed to the HTML templates.
type PageData struct {
	Title       string
	Storage     string
	User        *UserView
	FlashError  string
	FlashInfo   string
	SyncStatus  string
	LastSynced  string
	Heatmap     process.HeatmapData
	HeatmapSVG  template.HTML
	TopArtists  []ArtistCount
	RecentPlays []process.PlayRecord
}

// Renderer compiles and renders embedded HTML templates.
type Renderer struct {
	tmpl *template.Template
}

// NewRenderer parses the embedded templates once at startup.
func NewRenderer() (*Renderer, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Renderer{tmpl: tmpl}, nil
}

// RenderToBytes renders the layout template into a byte buffer so callers can
// compute ETags and handle template errors cleanly before writing headers.
func (r *Renderer) RenderToBytes(data PageData) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(16 * 1024)
	if err := r.tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// Render writes the rendered layout directly to w.
func (r *Renderer) Render(w io.Writer, data PageData) error {
	return r.tmpl.ExecuteTemplate(w, "layout", data)
}
