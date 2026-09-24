package optiimage_test

import (
	"context"
	"image"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"

	optiimage "github.com/Elagoht/collage-opti-image"
)

// TestPlugin_StaticBuildWritesTheImages is the reason the images are a mounted
// filesystem rather than a route.
//
// A static build copies every mount into its output after every page has rendered.
// By then this filesystem knows exactly which images the site uses, so the build
// writes real files under the names the pages already link — and the result needs
// nothing running behind it. A routed document could not be enumerated that way: its
// path is dynamic, and a build has no way to guess what would be asked for.
func TestPlugin_StaticBuildWritesTheImages(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)

	app, err := collage.New(&collage.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template: collage.TemplateConfig{FS: templates, Extension: ".html"},
		Plugins:  []collage.Plugin{optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(srv), CacheDir: t.TempDir()})},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := collage.NewPage("gallery").
		WithContent(collage.NewFragment("gallery", "pages/gallery.html").
			WithDataHandler(func(context.Context, *collage.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				return struct{ Body string }{
					Body: `<img src="` + srv.URL + `/a.png" width="200" height="150">`,
				}, nil, nil
			}).Build()).
		WithPath("en", "/gallery").
		Static().
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	report, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v (%v)", err, report.Errors)
	}

	// The page, with the rewritten src.
	html, err := os.ReadFile(filepath.Join(out, "gallery", "index.html"))
	if err != nil {
		t.Fatalf("read built page: %v", err)
	}
	src := srcOf(t, string(html))
	if !strings.HasPrefix(src, "/_image/") {
		t.Fatalf("built page src = %q, want it rewritten", src)
	}

	// And the file that src names, on disk, decodable, at the right size.
	onDisk := filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(src, "/")))
	raw, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("the built page links %q but the build wrote no such file: %v", src, err)
	}
	decoded, _, err := image.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("the written file is not an image: %v", err)
	}
	if got := decoded.Bounds().Size(); got.X != 200 || got.Y != 150 {
		t.Errorf("written image is %v, want 200x150", got)
	}
}

// TestPlugin_StaticBuildWritesNothingForAnUnusedMount guards the other direction: a
// site with no eligible images must not produce a directory of them.
func TestPlugin_StaticBuildWritesNoImagesWhenNoneAreUsed(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)

	app, err := collage.New(&collage.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template: collage.TemplateConfig{FS: templates, Extension: ".html"},
		Plugins:  []collage.Plugin{optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(srv), CacheDir: t.TempDir()})},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := collage.NewPage("gallery").
		WithContent(collage.NewFragment("gallery", "pages/gallery.html").
			WithDataHandler(func(context.Context, *collage.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				return struct{ Body string }{Body: `<p>no pictures here</p>`}, nil, nil
			}).Build()).
		WithPath("en", "/gallery").
		Static().
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(out, "_image"))
	if err == nil && len(entries) != 0 {
		t.Errorf("the build wrote %d image files for a site that uses none", len(entries))
	}
}

var _ = http.StatusOK
