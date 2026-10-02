package optiimage_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	optiimage "github.com/Elagoht/collage-opti-image"
)

func pngOf(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var red = color.NRGBA{R: 255, A: 255}

func staticFiles(files fstest.MapFS) map[string]fs.FS {
	return map[string]fs.FS{"/static/": files}
}

// A site's own image under a Files prefix is read from its filesystem and
// resized, with no origin configured and nothing fetched.
func TestPlugin_ResizesALocalFile(t *testing.T) {
	files := fstest.MapFS{"icons/avatar.png": {Data: pngOf(t, 512, 512, red)}}
	site := newSite(t, optiimage.Config{Files: staticFiles(files)},
		`<img src="/static/icons/avatar.png" width="64" height="64" alt="me">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if !strings.HasPrefix(src, "/_image/") {
		t.Fatalf("src = %q, want it rewritten under the plugin's prefix", src)
	}
	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", src, rec.Code, rec.Body.String())
	}
	decoded, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("served bytes are not an image: %v", err)
	}
	if got := decoded.Bounds().Size(); got.X != 64 || got.Y != 64 {
		t.Errorf("served image is %v, want 64x64", got)
	}
	if r, g, b, _ := decoded.At(32, 32).RGBA(); r>>8 != 255 || g != 0 || b != 0 {
		t.Errorf("served image is not the file's: rgb(%d, %d, %d)", r>>8, g>>8, b>>8)
	}
}

// A local file's content is in its name: the same file is the same name, a changed
// one a new name, so an immutable URL never comes to mean different bytes.
func TestPlugin_ALocalFileNamesItsContent(t *testing.T) {
	body := `<img src="/static/a.png" width="10" height="10">`
	name := func(c color.NRGBA) string {
		files := fstest.MapFS{"a.png": {Data: pngOf(t, 20, 20, c)}}
		return srcOf(t, get(t, newSite(t, optiimage.Config{Files: staticFiles(files)}, body), "/gallery").Body.String())
	}
	if first, second := name(red), name(red); first != second {
		t.Errorf("one file produced two names: %s and %s", first, second)
	}
	if name(red) == name(color.NRGBA{B: 255, A: 255}) {
		t.Error("a changed file kept its name")
	}
}

// Nothing outside the filesystem is reachable, however the path is spelled, and a
// path under no prefix, or naming no file, is left as written.
func TestPlugin_LocalPathsStayInTheirFilesystem(t *testing.T) {
	files := fstest.MapFS{"a.png": {Data: pngOf(t, 20, 20, red)}}
	for _, src := range []string{
		"/static/../a.png", "/static/./a.png", "/static//a.png", "//static/a.png",
		"/other/a.png", "/static/", "/static/missing.png", "static/a.png",
	} {
		site := newSite(t, optiimage.Config{Files: staticFiles(files)}, `<img src="`+src+`" width="10" height="10">`)
		if got := srcOf(t, get(t, site, "/gallery").Body.String()); got != src {
			t.Errorf("src %q became %q, want it left alone", src, got)
		}
	}
}

// A recipe a process recorded for a file that has changed since — read back from
// the disk cache by the next build — is refused rather than served under the old
// name with the new content.
func TestPlugin_AChangedLocalFileIsNotServedUnderItsOldName(t *testing.T) {
	dir := t.TempDir()
	body := `<img src="/static/a.png" width="10" height="10">`
	before := newSite(t, optiimage.Config{CacheDir: dir, Files: staticFiles(fstest.MapFS{"a.png": {Data: pngOf(t, 20, 20, red)}})}, body)
	old := srcOf(t, get(t, before, "/gallery").Body.String())

	after := newSite(t, optiimage.Config{CacheDir: dir, Files: staticFiles(fstest.MapFS{"a.png": {Data: pngOf(t, 20, 20, color.NRGBA{G: 255, A: 255})}})}, body)
	if rec := get(t, after, old); rec.Code == http.StatusOK {
		t.Errorf("GET %s = 200 with the changed file's content, want a refusal", old)
	}
	if fresh := srcOf(t, get(t, after, "/gallery").Body.String()); fresh == old {
		t.Error("the changed file kept its name")
	} else if rec := get(t, after, fresh); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d", fresh, rec.Code)
	}
}
