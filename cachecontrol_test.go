package optiimage_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"

	optiimage "github.com/Elagoht/collage-opti-image"
)

const immutable = "public, max-age=31536000, immutable"

func cacheControlOf(t *testing.T, h http.Handler, target string, header ...string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, rec.Header().Get("Cache-Control")
}

// An image fetched from an origin is named by its recipe, not its content: the
// origin can change the file under the same URL. It is cached for originMaxAge,
// a day by default, and never marked immutable.
func TestCacheControl_OriginImageExpires(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png" width="200" height="150">`)
	src := srcOf(t, get(t, site, "/gallery").Body.String())

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", src, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("origin image Cache-Control = %q, want a day and no immutable", cc)
	}
	code, cc := cacheControlOf(t, site, src, "If-None-Match", rec.Header().Get("ETag"))
	if code != http.StatusNotModified || cc != "public, max-age=86400" {
		t.Errorf("revalidated origin image = %d %q", code, cc)
	}
}

// An image of the site's own files has the file's content in its name, and keeps
// the year and immutable.
func TestCacheControl_LocalImageIsImmutable(t *testing.T) {
	files := staticFiles(fstest.MapFS{"a.png": {Data: pngOf(t, 128, 128, red)}})
	site := newSite(t, optiimage.Config{Files: files}, `<img src="/static/a.png" width="64" height="64">`)
	src := srcOf(t, get(t, site, "/gallery").Body.String())

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", src, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != immutable {
		t.Errorf("local image Cache-Control = %q, want %q", cc, immutable)
	}
	code, cc := cacheControlOf(t, site, src, "If-None-Match", rec.Header().Get("ETag"))
	if code != http.StatusNotModified || cc != immutable {
		t.Errorf("revalidated local image = %d %q", code, cc)
	}
	code, cc = cacheControlOf(t, site, src, "Range", "bytes=0-9")
	if code != http.StatusPartialContent || cc != immutable {
		t.Errorf("range of local image = %d %q", code, cc)
	}
}

// Both kinds on one page get their own header, and a name the plugin never
// minted is a 404 that promises nothing.
func TestCacheControl_MixedAndUnknown(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	files := staticFiles(fstest.MapFS{"a.png": {Data: pngOf(t, 128, 128, red)}})
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv), Files: files},
		`<img src="/static/a.png" width="64" height="64"><img src="`+srv.URL+`/a.png" width="200" height="150">`)
	page := get(t, site, "/gallery").Body.String()
	srcs := srcAttr.FindAllStringSubmatch(page, -1)
	if len(srcs) != 2 {
		t.Fatalf("srcs = %v", srcs)
	}
	if _, cc := cacheControlOf(t, site, srcs[0][1]); cc != immutable {
		t.Errorf("local image Cache-Control = %q", cc)
	}
	if _, cc := cacheControlOf(t, site, srcs[1][1]); cc != "public, max-age=86400" {
		t.Errorf("origin image Cache-Control = %q", cc)
	}
	code, cc := cacheControlOf(t, site, "/_image/00000000000000000000000000000000.png")
	if code != http.StatusNotFound || strings.Contains(cc, "immutable") {
		t.Errorf("unknown name = %d %q, want a 404 without immutable", code, cc)
	}
}

// After a restart the recipe is read back from disk, and a local image is still
// served as immutable without its page being rendered again.
func TestCacheControl_LocalImageAfterRestart(t *testing.T) {
	dir := t.TempDir()
	files := staticFiles(fstest.MapFS{"a.png": {Data: pngOf(t, 128, 128, red)}})
	first := newSite(t, optiimage.Config{Files: files, CacheDir: dir}, `<img src="/static/a.png" width="64" height="64">`)
	src := srcOf(t, get(t, first, "/gallery").Body.String())
	get(t, first, src)

	second := newSite(t, optiimage.Config{Files: files, CacheDir: dir}, `<img src="/static/a.png" width="64" height="64">`)
	if code, cc := cacheControlOf(t, second, src); code != http.StatusOK || cc != immutable {
		t.Errorf("after a restart = %d %q, want 200 %q", code, cc, immutable)
	}
}

// originMaxAge sets the origin images' lifetime, from Go or from JSON.
func TestCacheControl_OriginMaxAge(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	html := `<img src="` + srv.URL + `/a.png" width="200" height="150">`
	for _, c := range []struct {
		name   string
		cfg    optiimage.Config
		config string
		want   string
	}{
		{"go", optiimage.Config{AllowedOrigins: allow(srv), OriginMaxAge: optiimage.Duration(2 * time.Hour)}, "", "public, max-age=7200"},
		{"json text", optiimage.Config{AllowedOrigins: allow(srv)}, `{"originMaxAge": "1h"}`, "public, max-age=3600"},
		{"json number", optiimage.Config{AllowedOrigins: allow(srv)}, `{"originMaxAge": 60000000000}`, "public, max-age=60"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.CacheDir = t.TempDir()
			var config json.RawMessage
			if c.config != "" {
				config = json.RawMessage(c.config)
			}
			site := newSiteConfigured(t, optiimage.NewWith(c.cfg), config, html)
			src := srcOf(t, get(t, site, "/gallery").Body.String())
			if _, cc := cacheControlOf(t, site, src); cc != c.want {
				t.Errorf("Cache-Control = %q, want %q", cc, c.want)
			}
		})
	}
}

// An originMaxAge below a second, or negative, stops the application from
// starting: max-age counts whole seconds.
func TestCacheControl_OriginMaxAgeMustBeASecond(t *testing.T) {
	for _, config := range []string{`{"originMaxAge": "500ms"}`, `{"originMaxAge": "-1h"}`, `{"originMaxAge": "soon"}`} {
		_, err := collage.New(&collage.Config{
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			Template:     collage.TemplateConfig{FS: templates, Extension: ".html"},
			Plugins:      []collage.Plugin{optiimage.NewWith(optiimage.Config{AllowedOrigins: []optiimage.Origin{{Scheme: "https", Host: "img.example"}}, CacheDir: t.TempDir()})},
			PluginConfig: map[string]json.RawMessage{optiimage.Name: json.RawMessage(config)},
		})
		if err == nil {
			t.Errorf("%s: the application started", config)
		} else if !strings.Contains(err.Error(), "originMaxAge") {
			t.Errorf("%s: error %q does not name originMaxAge", config, err)
		}
	}
}
