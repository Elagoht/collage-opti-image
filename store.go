package optiimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// recipe is everything needed to produce one optimised image. It is what a name
// stands for.
type recipe struct {
	Source string
	Width  int
	Height int
	Format outputFormat
	// Alpha is the AlphaThreshold an auto format is settled with. Part of the
	// recipe, not read from the configuration when the image is made, so a name
	// always stands for one decision: change the threshold and the images it
	// affects get new names rather than new bytes under old ones.
	Alpha float64
}

// name is the file this recipe is served as.
//
// Content-addressed: the name is a hash of what the image is, so two pages asking
// for the same picture at the same size share one file, and a file's name cannot
// describe anything but its contents. That is what makes "cache forever" honest
// rather than optimistic.
//
// It also replaces a signature. An earlier design put the source URL in the path
// and signed it, because the endpoint would otherwise fetch whatever it was handed
// — a request-forgery primitive. Here the request carries no source at all: it
// carries a name, and a name the plugin did not mint stands for nothing. There is
// no signature because there is nothing to forge.
func (r recipe) name() string {
	key := r.Source + "|" + strconv.Itoa(r.Width) + "x" + strconv.Itoa(r.Height) + "|" + r.Format.name
	if r.Format.auto() {
		// The rule an auto format is settled by is part of what the image is.
		// "alpha2" names the rule that ignores nearly opaque pixels; the first one
		// counted any, so its images are made again under names of their own.
		key += "|alpha2:" + strconv.FormatFloat(r.Alpha, 'g', -1, 64)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16]) + r.Format.ext
}

// nameHexLen is the length of a name's hash part: sixteen bytes, hex-encoded.
const nameHexLen = 32

// validName reports whether name has the shape of one recipe.name mints.
//
// Checked before anything is looked up, because the cache directory holds more than
// images — recipes, and the temporary files a write goes through — and a request is
// a filename a caller chose. Without it "<name>.json" would serve a recipe, source
// URL and all, as if it were a picture.
func validName(name string) bool {
	ext := path.Ext(name)
	if _, known := formatsByExt[ext]; !known {
		return false
	}
	stem := strings.TrimSuffix(name, ext)
	if len(stem) != nameHexLen {
		return false
	}
	for i := range len(stem) {
		c := stem[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// persistedRecipe is a recipe as it is written beside the image it produces.
type persistedRecipe struct {
	Source string  `json:"source"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Format string  `json:"format"`
	Alpha  float64 `json:"alpha,omitempty"`
}

func (r recipe) marshal() ([]byte, error) {
	return json.Marshal(persistedRecipe{
		Source: r.Source, Width: r.Width, Height: r.Height, Format: r.Format.name, Alpha: r.Alpha,
	})
}

// unmarshalRecipe reads a persisted recipe back, and accepts it only as the recipe
// for name.
//
// The check that it hashes to name is what makes the file mean what its name says.
// The name is the recipe's content, so a file that describes something else — a
// stale one, a truncated one, one copied under the wrong name, one planted — is not
// a recipe this plugin wrote for that name, whatever it contains. It costs one hash.
func unmarshalRecipe(name string, data []byte) (recipe, bool) {
	var p persistedRecipe
	if err := json.Unmarshal(data, &p); err != nil {
		return recipe{}, false
	}
	format, known := formatsByName[p.Format]
	if !known || p.Width <= 0 || p.Height <= 0 {
		return recipe{}, false
	}
	r := recipe{Source: p.Source, Width: p.Width, Height: p.Height, Format: format, Alpha: p.Alpha}
	if r.name() != name {
		return recipe{}, false
	}
	return r, true
}

// recipeFile is the file a name's recipe is kept in, beside the image.
func recipeFile(name string) string { return name + ".json" }

// store holds the recipes the rewriter minted and the bytes they produced.
//
// Both halves are needed. The recipes make a name meaningful, and they are the only
// record that a name exists at all — which is also what lets a static build
// enumerate the images a site uses: by the time the builder copies mounts, every
// page has rendered and every recipe has been recorded.
//
// The bytes are held because a mounted file never enters the framework's page
// cache, so without them every request would fetch and resize again.
type store struct {
	mu       sync.RWMutex
	recipes  map[string]recipe
	bodies   map[string][]byte
	bytes    int64
	maxBytes int64
	// disk keeps produced images between restarts. Nil when the application asked
	// for memory only.
	disk    *disk
	modTime time.Time
}

func newStore(maxBytes int64, d *disk) *store {
	return &store{
		recipes:  make(map[string]recipe),
		bodies:   make(map[string][]byte),
		maxBytes: maxBytes,
		disk:     d,
		// One timestamp for every image, fixed at startup. A content-addressed
		// name cannot describe changing content, so If-Modified-Since has nothing
		// to be right or wrong about, and a per-file time would only vary with
		// when a reader first asked.
		modTime: time.Now(),
	}
}

// record remembers a recipe and returns its name.
//
// It is written to disk as well, the first time this process sees it. The page that
// links the name may outlive the process — the framework's disk cache keeps rendered
// HTML across restarts — and a name whose recipe lived only in memory was a 404
// after one, for every image nobody had happened to request before it.
func (s *store) record(r recipe) string {
	name := r.name()
	s.mu.Lock()
	_, known := s.recipes[name]
	s.recipes[name] = r
	s.mu.Unlock()

	if !known && s.disk != nil {
		if data, err := r.marshal(); err == nil {
			// Not worth failing a render over: the recipe is in memory, and the
			// only cost is the case this exists for.
			_ = s.disk.write(recipeFile(name), data)
		}
	}
	return name
}

// lookup returns the recipe a name stands for, from memory or from disk.
//
// Disk is the recipe an earlier process recorded for a page that is still being
// served. It is promoted into memory, so it is read once, and so a purge reaches it.
func (s *store) lookup(name string) (recipe, bool) {
	s.mu.RLock()
	r, ok := s.recipes[name]
	s.mu.RUnlock()
	if ok {
		return r, true
	}

	r, ok = s.persisted(name)
	if !ok {
		return recipe{}, false
	}
	s.mu.Lock()
	s.recipes[name] = r
	s.mu.Unlock()
	return r, true
}

// persisted reads the recipe an earlier process wrote for name. It takes no lock:
// it touches only the disk.
func (s *store) persisted(name string) (recipe, bool) {
	if s.disk == nil {
		return recipe{}, false
	}
	data, ok := s.disk.read(recipeFile(name))
	if !ok {
		return recipe{}, false
	}
	return unmarshalRecipe(name, data)
}

// names returns every recorded name, which is what a static build walks.
func (s *store) names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.recipes))
	for name := range s.recipes {
		out = append(out, name)
	}
	return out
}

// body returns a produced image, from memory or from disk.
//
// Disk is consulted second and promoted into memory, so a restarted process pays one
// read rather than a fetch, a decode and a resize — and pays it once.
func (s *store) body(name string) ([]byte, bool) {
	s.mu.RLock()
	b, ok := s.bodies[name]
	s.mu.RUnlock()
	if ok {
		return b, true
	}

	if s.disk == nil {
		return nil, false
	}
	b, ok = s.disk.read(name)
	if !ok {
		return nil, false
	}
	s.remember(name, b)
	return b, true
}

// keep stores produced bytes, bounded by total size.
//
// Eviction is "drop everything when full" rather than least-recently-used. That is
// crude and is the right shape here: a miss costs one fetch and one resize, a site's
// set of images is small and stable, and an LRU is a linked list, a map, a mutex and
// a class of bugs — for a plugin whose failure mode under memory pressure should be
// "slower", not "subtly wrong".
func (s *store) keep(name string, body []byte) {
	s.remember(name, body)
	if s.disk == nil {
		return
	}
	// A failed write is not worth failing a request over: the image is already
	// produced and in memory, and the only cost is producing it again after a
	// restart. newDisk reports the first failure and stops trying.
	_ = s.disk.write(name, body)
}

// remember holds body in memory, bounded by total size.
func (s *store) remember(name string, body []byte) {
	size := int64(len(body))
	if size > s.maxBytes {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.bodies[name]; exists {
		return
	}
	if s.bytes+size > s.maxBytes {
		s.bodies = make(map[string][]byte, len(s.bodies))
		s.bytes = 0
	}
	s.bodies[name] = body
	s.bytes += size
}

// forget drops produced bytes. An empty source drops all of them.
//
// The recipes survive deliberately, in memory and on disk: a page already rendered
// links these names, and forgetting what a name means would turn every one of those
// links into a 404 rather than into a re-fetch.
func (s *store) forget(source string) {
	// Read before the lock: persisted touches only the disk, and the recipes an
	// earlier process wrote are how a purge reaches an image on disk under a name
	// this process has not minted yet. Without them a wrong image survives the
	// purge that was meant to remove it.
	onDisk := make(map[string]recipe)
	if s.disk != nil {
		for _, name := range s.disk.recipeNames() {
			if r, ok := s.persisted(name); ok {
				onDisk[name] = r
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if source == "" {
		for name := range s.bodies {
			s.removeFromDisk(name)
		}
		for name := range s.recipes {
			s.removeFromDisk(name)
		}
		for name := range onDisk {
			s.removeFromDisk(name)
		}
		s.bodies = make(map[string][]byte)
		s.bytes = 0
		return
	}
	for name, r := range onDisk {
		if _, known := s.recipes[name]; !known {
			s.recipes[name] = r
		}
	}
	for name, r := range s.recipes {
		if r.Source != source {
			continue
		}
		if body, held := s.bodies[name]; held {
			s.bytes -= int64(len(body))
			delete(s.bodies, name)
		}
		s.removeFromDisk(name)
	}
}

// removeFromDisk drops a stored file. Called with s.mu held.
func (s *store) removeFromDisk(name string) {
	if s.disk != nil {
		s.disk.remove(name)
	}
}

// imageFS is the filesystem the optimised images are mounted as.
//
// A filesystem rather than a route, and that is what makes a static build work: the
// builder copies every mounted filesystem into its output *after* every page has
// rendered, so by the time it walks this one, the recipes name exactly the images
// the site uses. The build writes real files with the same names the pages already
// link, and the result needs nothing running behind it.
//
// At serve time the same filesystem produces on demand: Open fetches, resizes and
// remembers. One mechanism, two lifetimes.
type imageFS struct {
	plugin *Plugin
}

var (
	_ fs.FS        = (*imageFS)(nil)
	_ fs.ReadDirFS = (*imageFS)(nil)
)

// Open produces the image name stands for.
//
// The access control is that a name with no recipe produces nothing: the request
// carries a filename rather than a URL to fetch, so there is nothing for a caller to
// point somewhere else. A recipe is one this plugin recorded — in this process, or
// written by an earlier one beside the image and accepted only if it hashes to the
// name it is stored under. A forged name resolves to no recipe, and a zero recipe
// has an empty source that the allowlist refuses — so the refusal holds with or
// without the explicit check below. The allowlist is checked again when producing,
// so a recipe on disk cannot outlive the permission it was recorded under.
//
// The explicit fs.ErrNotExist is for the filesystem contract rather than for the
// response: a correct fs.FS says a missing file is missing, and a caller walking
// this — a static build, say — should see that rather than an origin error. Over
// HTTP it changes nothing, because the mount answers 404 for any Open failure,
// which also means an origin that is down is reported as a missing image. That
// conflation belongs to the mount and is the same for every asset it serves.
func (f *imageFS) Open(name string) (fs.File, error) {
	name = path.Clean(name)
	if name == "." {
		return &imageDir{fs: f}, nil
	}
	if !validName(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	// Disk first, and deliberately before the recipe.
	//
	// A restarted process has not rendered anything yet, so it knows no recipes —
	// but the file an earlier process wrote is still the right answer, because the
	// name is the content. Requiring a recipe here meant a 404 for every image
	// requested before its page happened to be rendered again: a CDN revalidating,
	// a browser with the page still cached, a crawler following a link. The file
	// exists only because this plugin wrote it, so serving it needs nothing else.
	if body, cached := f.plugin.store.body(name); cached {
		return newImageFile(name, body, f.plugin.store.modTime), nil
	}

	r, known := f.plugin.store.lookup(name)
	if !known {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	body, err := f.plugin.produce(f.plugin.fetchContext(), r)
	if err != nil {
		f.plugin.log.Warn("opti-image: could not produce image",
			"source", r.Source, "width", r.Width, "height", r.Height, "err", err)
		// Not ErrNotExist: the file is one this plugin promised, and reporting it
		// missing would let a build quietly write a site with fewer images than
		// its pages reference.
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	f.plugin.store.keep(name, body)
	return newImageFile(name, body, f.plugin.store.modTime), nil
}

// ReadDir lists the images the site has asked for, which is how a static build
// discovers them. Only the root is a directory; there are no others.
func (f *imageFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	if path.Clean(dir) != "." {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrNotExist}
	}

	names := f.plugin.store.names()
	entries := make([]fs.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, imageEntry{fs: f, name: name})
	}
	return entries, nil
}

var errNotADirectory = errors.New("opti-image: not a directory")

// imageEntry is one image as a directory entry. Info produces the image, because
// a size is not knowable without it — which is also why a build walking this
// filesystem does the work rather than merely listing it.
type imageEntry struct {
	fs   *imageFS
	name string
}

func (e imageEntry) Name() string      { return e.name }
func (e imageEntry) IsDir() bool       { return false }
func (e imageEntry) Type() fs.FileMode { return 0 }

func (e imageEntry) Info() (fs.FileInfo, error) {
	file, err := e.fs.Open(e.name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

// imageDir is the mount's root, which exists only so WalkDir has somewhere to start.
type imageDir struct{ fs *imageFS }

func (d *imageDir) Stat() (fs.FileInfo, error) { return imageDirInfo{}, nil }
func (d *imageDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: ".", Err: errNotADirectory}
}
func (d *imageDir) Close() error { return nil }
func (d *imageDir) ReadDir(int) ([]fs.DirEntry, error) {
	return d.fs.ReadDir(".")
}

type imageDirInfo struct{}

func (imageDirInfo) Name() string       { return "." }
func (imageDirInfo) Size() int64        { return 0 }
func (imageDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (imageDirInfo) ModTime() time.Time { return time.Time{} }
func (imageDirInfo) IsDir() bool        { return true }
func (imageDirInfo) Sys() any           { return nil } // any: fs.FileInfo's own signature

// imageFile is produced image bytes presented as an fs.File. It is a *bytes.Reader
// under the hood, so the mount's http.ServeContent gets a real io.ReadSeeker and
// Range requests address the bytes actually served.
type imageFile struct {
	*readSeekCloser
	info imageInfo
}

func newImageFile(name string, body []byte, mod time.Time) *imageFile {
	return &imageFile{
		readSeekCloser: newReadSeekCloser(body),
		info:           imageInfo{name: name, size: int64(len(body)), mod: mod},
	}
}

func (f *imageFile) Stat() (fs.FileInfo, error) { return f.info, nil }

type imageInfo struct {
	name string
	size int64
	mod  time.Time
}

func (i imageInfo) Name() string       { return i.name }
func (i imageInfo) Size() int64        { return i.size }
func (i imageInfo) Mode() fs.FileMode  { return 0o444 }
func (i imageInfo) ModTime() time.Time { return i.mod }
func (i imageInfo) IsDir() bool        { return false }
func (i imageInfo) Sys() any           { return nil } // any: fs.FileInfo's own signature

func ensureNoError(err error) error { return fmt.Errorf("unreachable: %w", err) }
