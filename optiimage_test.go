package optiimage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"

	optiimage "github.com/Elagoht/collage-opti-image"
)

// origin serves one PNG and counts requests, so a test can tell a fetch from a
// cache hit. It records the paths it was asked for, so a test can tell which URL
// was fetched and not only that one was.
type origin struct {
	hits atomic.Int64
	body []byte

	mu    sync.Mutex
	paths []string
}

func (o *origin) requested() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.paths...)
}

func newOrigin(t *testing.T, w, h int) (*origin, *httptest.Server) {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			// A gradient, so a resize that silently returns the source is visible
			// in the output's dimensions rather than only in its bytes.
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}

	o := &origin{body: buf.Bytes()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.hits.Add(1)
		o.mu.Lock()
		o.paths = append(o.paths, r.URL.Path)
		o.mu.Unlock()
		if r.URL.Path == "/missing.png" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		if r.URL.Path == "/notanimage.png" {
			w.Write([]byte("this is not an image"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(o.body)
	}))
	t.Cleanup(srv.Close)
	return o, srv
}

var templates = fstest.MapFS{
	// safeHTML, because the test's whole input is the markup: escaped, there would
	// be no <img> element for the plugin to find.
	"pages/gallery.html": &fstest.MapFile{Data: []byte(`<html><body>{{safeHTML .Body}}</body></html>`)},
}

// newSite builds a site whose one page renders bodyHTML verbatim.
//
// The cache directory is the test's own unless the test names one. The default is
// .cache/opti-image in the working directory, which for a test is the checkout: one
// run's images would be the next run's disk hits, and a test counting origin
// fetches would be counting what an earlier run left behind.
func newSite(t *testing.T, cfg optiimage.Config, bodyHTML string) http.Handler {
	t.Helper()
	if cfg.CacheDir == "" {
		cfg.CacheDir = t.TempDir()
	}
	return newSiteWith(t, optiimage.NewWith(cfg), bodyHTML)
}

// newSiteWith builds the site around a plugin the caller keeps a handle on, which
// is what a test calling Purge needs.
func newSiteWith(t *testing.T, plug *optiimage.Plugin, bodyHTML string) http.Handler {
	t.Helper()
	return newSiteConfigured(t, plug, nil, bodyHTML)
}

// newSiteConfigured also hands the application a plugin configuration, the way a
// configuration file would.
func newSiteConfigured(t *testing.T, plug *optiimage.Plugin, pluginConfig json.RawMessage, bodyHTML string) http.Handler {
	t.Helper()

	var plugins map[string]json.RawMessage
	if pluginConfig != nil {
		plugins = map[string]json.RawMessage{optiimage.Name: pluginConfig}
	}
	app, err := collage.New(&collage.Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template:     collage.TemplateConfig{FS: templates, Extension: ".html"},
		Plugins:      []collage.Plugin{plug},
		PluginConfig: plugins,
		Cache:        collage.CacheConfig{Enabled: true, Type: "memory", MaxEntries: 64},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page := collage.NewPage("gallery").
		WithContent(collage.NewFragment("gallery", "pages/gallery.html").
			WithDataHandler(func(context.Context, *collage.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				return struct{ Body string }{Body: bodyHTML}, nil, nil
			}).
			Build()).
		WithPath("en", "/gallery").
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app.Handler()
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

var srcAttr = regexp.MustCompile(`src="([^"]*)"`)

func srcOf(t *testing.T, body string) string {
	t.Helper()
	m := srcAttr.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no src attribute in:\n%s", body)
	}
	return m[1]
}

func allow(srv *httptest.Server) []optiimage.Origin {
	u, _ := url.Parse(srv.URL)
	return []optiimage.Origin{{Scheme: u.Scheme, Host: u.Host}}
}

func TestPlugin_RewritesAndServesADeclaredSizeImage(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/photo.png" width="200" height="150" alt="x">`)

	body := get(t, site, "/gallery").Body.String()
	src := srcOf(t, body)
	if !strings.HasPrefix(src, "/_image/") {
		t.Fatalf("src = %q, want it rewritten under the plugin's prefix", src)
	}
	if o.hits.Load() != 0 {
		t.Errorf("the origin was fetched during the render (%d times); the page must not wait on images", o.hits.Load())
	}

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", src, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png — a PNG must not become a JPEG and lose its transparency", ct)
	}

	decoded, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("served bytes are not an image: %v", err)
	}
	if got := decoded.Bounds().Size(); got.X != 200 || got.Y != 150 {
		t.Errorf("served image is %v, want 200x150", got)
	}
}

// html/template writes a "+" inside an attribute as "&#43;". The attribute value is
// HTML, so the URL it holds is the decoded one: taken verbatim, "&#43;" reaches the
// origin as "&" and a fragment, and a CMS cover whose path has a "+" is a 404.
func TestPlugin_DecodesTheSrcBeforeFetchingIt(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a&#43;b.jpg" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if rec := get(t, site, src); rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", src, rec.Code)
	}
	if got := o.requested(); len(got) != 1 || got[0] != "/a+b.jpg" {
		t.Errorf("the origin was asked for %q, want [/a+b.jpg]", got)
	}
}

// What is written back is escaped, so a prefix the configuration chose cannot end
// the attribute it is written into.
func TestPlugin_EscapesTheRewrittenSrc(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv), Prefix: `/img&"x/`},
		`<img src="`+srv.URL+`/a.png" width="200" height="150" alt="kept">`)

	body := get(t, site, "/gallery").Body.String()
	if !strings.Contains(body, `src="/img&amp;&#34;x/`) {
		t.Errorf("the rewritten src is not escaped:\n%s", body)
	}
	if !strings.Contains(body, `alt="kept"`) {
		t.Errorf("the tag was corrupted:\n%s", body)
	}
}

func TestPlugin_IgnoresImagesWithoutBothDimensions(t *testing.T) {
	_, srv := newOrigin(t, 40, 30)
	for _, tag := range []string{
		`<img src="` + srv.URL + `/a.png" width="10">`,
		`<img src="` + srv.URL + `/a.png" height="10">`,
		`<img src="` + srv.URL + `/a.png">`,
		`<img src="` + srv.URL + `/a.png" width="100%" height="50">`,
	} {
		site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)}, tag)
		if src := srcOf(t, get(t, site, "/gallery").Body.String()); strings.HasPrefix(src, "/_image/") {
			t.Errorf("%s was rewritten; without both pixel dimensions the target size is a guess", tag)
		}
	}
}

func TestPlugin_IgnoresImagesFromOtherOrigins(t *testing.T) {
	_, srv := newOrigin(t, 40, 30)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="https://elsewhere.example/a.png" width="20" height="10">`)

	if src := srcOf(t, get(t, site, "/gallery").Body.String()); src != "https://elsewhere.example/a.png" {
		t.Errorf("src = %q, want it left alone", src)
	}
}

func TestPlugin_UserinfoCannotForgeAnAllowedOrigin(t *testing.T) {
	// "https://allowed.example@evil.example/x" has host evil.example and begins
	// with the allowed host as a string. A prefix check on the raw URL allows it.
	_, srv := newOrigin(t, 40, 30)
	u, _ := url.Parse(srv.URL)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+u.Scheme+`://`+u.Host+`@evil.example/a.png" width="20" height="10">`)

	if src := srcOf(t, get(t, site, "/gallery").Body.String()); strings.HasPrefix(src, "/_image/") {
		t.Errorf("a userinfo-disguised host was accepted: %q", src)
	}
}

func TestPlugin_ServesFromCacheOnTheSecondRequest(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	get(t, site, src)
	after := o.hits.Load()
	if after == 0 {
		t.Fatal("the first request did not fetch the origin")
	}

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("second request = %d", rec.Code)
	}
	if o.hits.Load() != after {
		t.Error("the second request fetched the origin again")
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "public") {
		t.Errorf("Cache-Control = %q, want a public max-age — an image a client re-fetches is not optimised", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag, so a conditional request cannot be answered")
	}
}

func TestPlugin_OriginFailuresDoNotTakeThePageDown(t *testing.T) {
	_, srv := newOrigin(t, 40, 30)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/missing.png" width="20" height="10">`)

	// The page itself renders: the rewrite does not fetch.
	if rec := get(t, site, "/gallery"); rec.Code != http.StatusOK {
		t.Fatalf("the page = %d, want 200 even though its image is missing", rec.Code)
	}
	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if rec := get(t, site, src); rec.Code == http.StatusOK {
		t.Error("a missing origin image was served as a success")
	}
}

func TestPlugin_RefusesSomethingThatIsNotAnImage(t *testing.T) {
	_, srv := newOrigin(t, 40, 30)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/notanimage.png" width="20" height="10">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if rec := get(t, site, src); rec.Code == http.StatusOK {
		t.Error("a non-image body was served as an image")
	}
}

func TestPlugin_RefusesAnOversizeSource(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv), MaxSourceBytes: 64}, // smaller than the fixture
		`<img src="`+srv.URL+`/a.png" width="20" height="10">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if rec := get(t, site, src); rec.Code == http.StatusOK {
		t.Error("a source over the byte limit was served")
	}
}

func TestPlugin_RefusesTooManyPixels(t *testing.T) {
	// The decompression bomb: a small file that decodes to an enormous bitmap. The
	// byte limit cannot see it, which is why the pixel limit is checked from the
	// header before anything is decoded.
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv), MaxPixels: 100},
		`<img src="`+srv.URL+`/a.png" width="20" height="10">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if rec := get(t, site, src); rec.Code == http.StatusOK {
		t.Error("a source over the pixel limit was decoded and served")
	}
}

func TestPlugin_DoesNothingWithNoAllowedOrigins(t *testing.T) {
	// An empty allowlist must disable the plugin, never mean "any host". A fetcher
	// defaulting to "anywhere" is an SSRF primitive wearing a feature's name.
	_, srv := newOrigin(t, 40, 30)
	site := newSite(t, optiimage.Config{}, `<img src="`+srv.URL+`/a.png" width="20" height="10">`)

	if src := srcOf(t, get(t, site, "/gallery").Body.String()); strings.HasPrefix(src, "/_image/") {
		t.Errorf("src = %q, want it left alone when nothing is allowed", src)
	}
	if rec := get(t, site, "/_image/png/anything"); rec.Code == http.StatusOK {
		t.Error("the image route answers although the plugin is inactive")
	}
}

func TestPlugin_LeavesDataSrcAlone(t *testing.T) {
	// "data-src=" contains "src=". Rewriting the wrong attribute breaks whatever
	// lazy-loading script the page uses and leaves the real src untouched.
	_, srv := newOrigin(t, 40, 30)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img data-src="`+srv.URL+`/a.png" width="20" height="10">`)

	body := get(t, site, "/gallery").Body.String()
	if strings.Contains(body, "/_image/") {
		t.Errorf("data-src was rewritten:\n%s", body)
	}
}

// A name the plugin never minted stands for nothing, which is the whole of the
// access control now: the request carries a filename, not a URL to fetch, so there
// is nothing for a caller to point somewhere else.
func TestPlugin_AnInventedNameIsNotFound(t *testing.T) {
	o, srv := newOrigin(t, 40, 30)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png" width="20" height="10">`)

	for _, invented := range []string{
		"/_image/deadbeefdeadbeefdeadbeefdeadbeef.png",
		"/_image/../../etc/passwd",
		"/_image/",
	} {
		// A mount answers 404 for any Open failure, so this pins the outcome
		// rather than the mechanism: whatever goes wrong with a name nothing
		// minted, the reader is told the file is not there and the origin is never
		// contacted.
		if rec := get(t, site, invented); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", invented, rec.Code)
		}
	}
	if o.hits.Load() != 0 {
		t.Errorf("an invented name caused %d origin fetches", o.hits.Load())
	}
}

func TestPlugin_NamesAreContentAddressed(t *testing.T) {
	// Two pages asking for the same picture at the same size share one file, and a
	// different size is a different file. That is what makes "cache forever"
	// honest: a name cannot come to mean different bytes.
	_, srv := newOrigin(t, 400, 300)

	same := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png" width="200" height="150">`+
			`<img src="`+srv.URL+`/a.png" width="200" height="150">`)
	body := get(t, same, "/gallery").Body.String()
	names := srcAttr.FindAllStringSubmatch(body, -1)
	if len(names) != 2 {
		t.Fatalf("got %d img elements, want 2", len(names))
	}
	if names[0][1] != names[1][1] {
		t.Errorf("one picture at one size produced two names: %q and %q", names[0][1], names[1][1])
	}

	other := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png" width="100" height="75">`)
	if got := srcOf(t, get(t, other, "/gallery").Body.String()); got == names[0][1] {
		t.Error("a different size produced the same name")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage { // any: restates encoding/json's own parameter type
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// The images live in the plugin's own store, not the framework's cache — a mounted
// file never enters that — so purging is a method rather than a dependency tag.
// Without it the only fix for an origin that served a wrong file would be restarting
// the process, and these are cached for a year.
func TestPlugin_ProducedImagesCanBePurged(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	p := optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(srv), CacheDir: t.TempDir()})
	site := newSiteWith(t, p, `<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	get(t, site, src)
	cached := o.hits.Load()

	get(t, site, src)
	if o.hits.Load() != cached {
		t.Fatal("the second request refetched, so nothing was cached and this proves nothing")
	}

	p.Purge()

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("after Purge the image is %d, want 200 — purging drops the bytes, not the name", rec.Code)
	}
	if o.hits.Load() == cached {
		t.Error("the image survived Purge")
	}
}

func TestPlugin_OneSourceCanBePurgedAlone(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	p := optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(srv), CacheDir: t.TempDir()})
	site := newSiteWith(t, p,
		`<img src="`+srv.URL+`/a.png" width="200" height="150">`+
			`<img src="`+srv.URL+`/b.png" width="200" height="150">`)

	matches := srcAttr.FindAllStringSubmatch(get(t, site, "/gallery").Body.String(), -1)
	if len(matches) != 2 {
		t.Fatalf("got %d images, want 2", len(matches))
	}
	first, second := matches[0][1], matches[1][1]
	get(t, site, first)
	get(t, site, second)
	cached := o.hits.Load()

	p.PurgeSource(srv.URL + "/a.png")

	get(t, site, second)
	if o.hits.Load() != cached {
		t.Error("purging one source dropped another's image")
	}
	get(t, site, first)
	if o.hits.Load() == cached {
		t.Error("the purged source was still served from the store")
	}
}

// NewWith is a starting point the JSON configuration is decoded over: a key the
// JSON has replaces the value entirely — an allowedOrigins array is not merged
// with NewWith's — and a key it lacks leaves NewWith's value alone.
func TestNewWith_TheJSONReplacesWhatItNamesAndKeepsTheRest(t *testing.T) {
	_, fromGo := newOrigin(t, 40, 30)
	_, fromJSON := newOrigin(t, 40, 30)
	markup := `<img src="` + fromGo.URL + `/a.png" width="20" height="10">` +
		`<img src="` + fromJSON.URL + `/a.png" width="20" height="10">`
	rewritten := func(h http.Handler) []bool {
		var out []bool
		for _, m := range srcAttr.FindAllStringSubmatch(get(t, h, "/gallery").Body.String(), -1) {
			out = append(out, strings.HasPrefix(m[1], "/_image/"))
		}
		return out
	}

	replaced := newSiteConfigured(t,
		optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(fromGo), CacheDir: t.TempDir()}),
		mustJSON(t, map[string][]optiimage.Origin{"allowedOrigins": allow(fromJSON)}), markup)
	if got := rewritten(replaced); len(got) != 2 || got[0] || !got[1] {
		t.Errorf("with allowedOrigins in the JSON, rewritten = %v, want [false true]", got)
	}

	kept := newSiteConfigured(t,
		optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(fromGo), CacheDir: t.TempDir()}),
		json.RawMessage(`{"quality":60}`), markup)
	if got := rewritten(kept); len(got) != 2 || !got[0] || got[1] {
		t.Errorf("without allowedOrigins in the JSON, rewritten = %v, want [true false]", got)
	}
}

// Decoding over NewWith's value must not write into the caller's slice.
// encoding/json reuses a slice's backing array, so without a copy the JSON's
// origins overwrote the ones the caller still held.
func TestNewWith_DoesNotWriteIntoTheCallersOrigins(t *testing.T) {
	_, srv := newOrigin(t, 40, 30)
	mine := []optiimage.Origin{
		{Scheme: "https", Host: "mine.example"},
		{Scheme: "https", Host: "also-mine.example"},
	}
	newSiteConfigured(t,
		optiimage.NewWith(optiimage.Config{AllowedOrigins: mine, CacheDir: t.TempDir()}),
		mustJSON(t, map[string][]optiimage.Origin{"allowedOrigins": allow(srv)}), `<p></p>`)

	if mine[0].Host != "mine.example" {
		t.Errorf("the caller's origins were overwritten: %v", mine)
	}
}
