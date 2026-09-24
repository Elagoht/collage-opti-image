package optiimage_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	if n := len(imagesIn(t, dir)); n != 1 {
		t.Fatalf("cache dir holds %d images, want 1", n)
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
	if n := len(imagesIn(t, dir)); n != 1 {
		t.Fatalf("cache dir holds %d images, want 1", n)
	}

	p.Purge()

	if n := len(imagesIn(t, dir)); n != 0 {
		t.Errorf("cache dir still holds %d images after Purge", n)
	}
	// The recipe stays: the page still links the name, and it should mean a
	// re-fetch rather than a 404.
	if n := len(recipesIn(t, dir)); n != 1 {
		t.Errorf("cache dir holds %d recipes after Purge, want 1", n)
	}
}

// imagesIn and recipesIn split the cache directory into what is served and what
// says how to produce it.
func imagesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, name := range filesIn(t, dir) {
		if !strings.HasSuffix(name, ".json") {
			out = append(out, name)
		}
	}
	return out
}

func recipesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, name := range filesIn(t, dir) {
		if strings.HasSuffix(name, ".json") {
			out = append(out, name)
		}
	}
	return out
}

func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
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

// TestDisk_ProducesAnImageNoEarlierProcessProduced is the restart case the stored
// image does not cover: a page rendered and cached, its image never requested.
//
// The framework's disk cache keeps the HTML across the restart, so the page still
// links the name — and with the recipe only in the old process's memory, the image
// was a 404 until the page happened to be rendered again.
func TestDisk_ProducesAnImageNoEarlierProcessProduced(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()
	markup := `<img src="` + srv.URL + `/a.png" width="200" height="150">`

	first := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)
	src := srcOf(t, get(t, first, "/gallery").Body.String())
	if o.hits.Load() != 0 {
		t.Fatalf("the render fetched the origin")
	}

	second := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)
	rec := get(t, second, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the page from before the restart links this name", rec.Code)
	}
	if o.hits.Load() != 1 {
		t.Errorf("origin fetches = %d, want 1", o.hits.Load())
	}
}

// A recipe on disk was recorded under whatever the allowlist was then. It must not
// outlive the permission: narrowing the allowlist has to stop the fetch.
func TestDisk_APersistedRecipeIsCheckedAgainstTheAllowlist(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	_, other := newOrigin(t, 40, 30)
	dir := t.TempDir()
	markup := `<img src="` + srv.URL + `/a.png" width="200" height="150">`

	first := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)
	src := srcOf(t, get(t, first, "/gallery").Body.String())

	narrowed := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(other), CacheDir: dir,
	}), markup)
	if rec := get(t, narrowed, src); rec.Code == http.StatusOK {
		t.Error("a persisted recipe for an origin no longer allowed was produced")
	}
	if o.hits.Load() != 0 {
		t.Errorf("origin fetches = %d, want 0", o.hits.Load())
	}
}

// A recipe file is accepted only as the recipe for the name it is stored under,
// which is what the name — a hash of the recipe — already says it is. One that
// describes something else was not written by this plugin for that name.
func TestDisk_ARecipeThatDoesNotMatchItsNameIsIgnored(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()

	first := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), `<img src="`+srv.URL+`/a.png" width="200" height="150">`)
	src := srcOf(t, get(t, first, "/gallery").Body.String())

	recipes := recipesIn(t, dir)
	if len(recipes) != 1 {
		t.Fatalf("cache dir holds %d recipes, want 1", len(recipes))
	}
	planted := `{"source":"` + srv.URL + `/b.png","width":4000,"height":3000,"format":"png"}`
	if err := os.WriteFile(filepath.Join(dir, recipes[0]), []byte(planted), 0o644); err != nil {
		t.Fatalf("plant recipe: %v", err)
	}

	second := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), `<p>nothing rendered</p>`)
	if rec := get(t, second, src); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a recipe that is not the name's", rec.Code)
	}
	if o.hits.Load() != 0 {
		t.Errorf("origin fetches = %d, want 0", o.hits.Load())
	}
}

// The cache directory holds recipes too, and a request is a filename the caller
// chose. Only a name the plugin mints is served; a recipe is not a picture.
func TestDisk_TheRecipesAreNotServed(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()
	site := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), `<img src="`+srv.URL+`/a.png" width="200" height="150">`)
	src := srcOf(t, get(t, site, "/gallery").Body.String())

	if rec := get(t, site, src+".json"); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s.json = %d, want 404:\n%s", src, rec.Code, rec.Body.String())
	}
}

// PurgeSource reaches an image an earlier process produced, through the recipe it
// left beside it. Without that, the wrong image the purge was meant to remove is
// served again from disk the moment it is asked for.
func TestDisk_PurgeSourceReachesAnEarlierProcesssImage(t *testing.T) {
	o, srv := newOrigin(t, 400, 300)
	dir := t.TempDir()
	markup := `<img src="` + srv.URL + `/a.png" width="200" height="150">`

	first := newSiteWith(t, optiimage.NewWith(optiimage.Config{
		AllowedOrigins: allow(srv), CacheDir: dir,
	}), markup)
	get(t, first, srcOf(t, get(t, first, "/gallery").Body.String()))
	if n := len(imagesIn(t, dir)); n != 1 {
		t.Fatalf("cache dir holds %d images, want 1", n)
	}

	p := optiimage.NewWith(optiimage.Config{AllowedOrigins: allow(srv), CacheDir: dir})
	newSiteWith(t, p, markup)
	p.PurgeSource(srv.URL + "/a.png")

	if n := len(imagesIn(t, dir)); n != 0 {
		t.Errorf("cache dir holds %d images after PurgeSource, want 0", n)
	}
	if o.hits.Load() != 1 {
		t.Errorf("origin fetches = %d, want 1", o.hits.Load())
	}
}
