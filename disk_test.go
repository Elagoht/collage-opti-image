package optiimage_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	optiimage "github.com/Elagoht/collage-opti-image"
)

// TestDisk_SurvivesARestart is what the disk cache is for. The names are
// content-addressed, so a file a previous process wrote is still the right answer —
// which is why picking it up needs no validation and no expiry.
func TestDisk_SurvivesARestart(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()
	markup := `<img src="` + srv.URL + `/a.png" width="200" height="150">`

	first := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)

	src := srcOf(t, get(t, first, "/gallery").Body.String())
	get(t, first, src)
	if o.hits.Load() != 1 {
		t.Fatalf("origin fetches = %d, want 1", o.hits.Load())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache dir holds %d files, want 1", len(entries))
	}

	// A second application over the same directory: a restart, as far as anything
	// here can tell.
	second := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)

	again := srcOf(t, get(t, second, "/gallery").Body.String())
	if again != src {
		t.Fatalf("the name changed across restarts: %q then %q", src, again)
	}

	rec := get(t, second, again)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if o.hits.Load() != 1 {
		t.Errorf("origin fetches = %d, want 1 — the restart refetched what was already on disk", o.hits.Load())
	}
}

func TestDisk_PurgeRemovesTheFiles(t *testing.T) {
	// A purge that leaves the file behind is not a purge: the next process would
	// serve exactly the image the purge was meant to remove.
	_, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()
	p := optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(srv), CacheDir: dir})
	site := newSiteWith(t, p, `<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	get(t, site, srcOf(t, get(t, site, "/gallery").Body.String()))
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("cache dir holds %d files, want 1", len(entries))
	}

	p.Purge()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("cache dir still holds %d files after Purge", len(entries))
	}
}

func TestDisk_UnwritableDirectoryDegradesToMemory(t *testing.T) {
	// A read-only deployment is a normal deployment. Failing to start on one, or
	// retrying the same failing write for every image, is worse than keeping
	// everything in memory and saying so once.
	_, srv := newOrigin(t, 400, 300)

	parent := t.TempDir()
	blocked := filepath.Join(parent, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant a file where the directory would go: %v", err)
	}

	site := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: filepath.Join(blocked, "cache"),
	}), `<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unusable cache directory must not cost the image", rec.Code)
	}
	if get(t, site, src).Code != http.StatusOK {
		t.Error("the second request failed, so the memory cache is not covering for the disk")
	}
}

func TestDisk_NoDiskCacheWritesNothing(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()

	site := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir, NoDiskCache: true,
	}), `<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	get(t, site, srcOf(t, get(t, site, "/gallery").Body.String()))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("NoDiskCache wrote %d files", len(entries))
	}
}

// TestDisk_ServesAStoredImageWithoutRenderingItsPage is the case the first restart
// test missed by rendering the gallery before asking for the image.
//
// A restarted process knows no recipes until something renders. Meanwhile a CDN
// revalidating, a browser holding the page from before, or a crawler following a
// link all ask for the image directly — and got a 404, with the file sitting on
// disk the whole time.
func TestDisk_ServesAStoredImageWithoutRenderingItsPage(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()
	markup := `<img src="` + srv.URL + `/a.png" width="200" height="150">`

	first := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)
	src := srcOf(t, get(t, first, "/gallery").Body.String())
	get(t, first, src)

	// A fresh application over the same directory, asked for the image and nothing
	// else. No page is rendered, so no recipe exists.
	second := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)

	rec := get(t, second, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the file is on disk and the name is its content", rec.Code)
	}
	if o.hits.Load() != 1 {
		t.Errorf("origin fetches = %d, want 1", o.hits.Load())
	}
}
