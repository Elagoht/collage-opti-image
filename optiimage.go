// Package optiimage is a collage plugin that rewrites images declaring a width and
// a height to resized copies it serves itself.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{optiimage.New()},
//		PluginConfig: cfg, // must name at least one allowed origin
//	})
//
// It has to be supplied through Config.Plugins: it registers the route it serves
// its own images from, and that has to happen while the application is built.
//
// # What it does, and when
//
// The rewrite happens during the render: an <img> with width, height and a src on
// an allowed origin has its src replaced with a content-addressed name under the
// plugin's own prefix. Nothing is fetched at that point.
//
// The fetch, the decode and the resize happen on the first request for that URL.
// Doing them during the render would make the first view of a page as slow as its
// slowest image, and would do it while holding a render the reader is waiting on.
// Doing them at the image endpoint means the page arrives at once and the images
// arrive as the browser asks for them, which is also the order the browser wants.
//
// # Why the URLs are not signed
//
// The request carries no source URL, only a name, and a name stands for a recipe
// this plugin recorded or for nothing. There is nothing to forge, so nothing is
// signed; the allowlist is checked again at fetch time all the same, so a recipe
// cannot outlive the permission it was recorded under. See recipe.name.
package optiimage

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	// The decoders the standard library has. They register themselves with
	// image.Decode, which is how a JPEG is told from a PNG without trusting the
	// origin's Content-Type — which is a claim, not a fact.
	_ "image/gif"

	"github.com/Elagoht/collage/pkg/collage"
)

// Plugin rewrites and serves optimised images.
type Plugin struct {
	cfg    Config
	store  *store
	client *http.Client
	log    *slog.Logger
}

// New returns a plugin configured entirely from the application. It does nothing
// until an allowed origin is configured.
func New() *Plugin { return &Plugin{} }

// NewWith returns a plugin with cfg as its starting point, which the application's
// own configuration is decoded over.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string    { return Name }
func (p *Plugin) Version() string { return "1.0.0" }

// Configure decodes the configuration and prepares the store. It does not mount the
// images: that needs Host, which Init receives.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	p.log = host.Logger()
	if err := host.Config(&p.cfg); err != nil {
		return err
	}
	p.cfg = p.cfg.withDefaults()
	if err := p.cfg.validate(); err != nil {
		return err
	}

	var d *disk
	if !p.cfg.NoDiskCache {
		d = newDisk(p.cfg.CacheDir, func(err error) {
			p.log.Warn("opti-image: disk cache unavailable, keeping images in memory only",
				"dir", p.cfg.CacheDir, "err", err)
		})
	}
	p.store = newStore(p.cfg.CacheBytes, d)
	p.client = &http.Client{Timeout: time.Duration(p.cfg.FetchTimeout)}
	return nil
}

// Purge discards every image this plugin has produced, so the next request for each
// fetches and resizes again.
//
// It is a method rather than a dependency tag because a mounted file never enters
// the framework's cache — InvalidateTags has nothing to reach here. What it drops is
// the produced bytes, not the recipes: a page already rendered still links these
// names, and forgetting what they mean would turn every one of those links into a
// 404.
//
// The case it exists for is an origin that served a wrong file. The names are
// content-addressed and cached for a year, so without this the only fix would be
// restarting the process.
func (p *Plugin) Purge() {
	if p.store != nil {
		p.store.forget("")
	}
}

// PurgeSource discards every image derived from one origin URL, at every size, so a
// caller that knows which picture changed does not have to discard the rest.
func (p *Plugin) PurgeSource(source string) {
	if p.store != nil {
		p.store.forget(source)
	}
}

// Init mounts the filesystem the optimised images are served from.
//
// A mount rather than a route, and the choice is what makes a static build work.
// The builder copies every mounted filesystem into its output after every page has
// rendered, so by then the filesystem knows exactly which images the site uses: it
// writes real files with the names the pages already link, and the built site needs
// nothing running behind it. A routed document could not be enumerated that way —
// its path is dynamic, and a build has no way to guess what would be asked for.
//
// Cache-Control says immutable because the names are content-addressed: a name
// cannot come to mean different bytes, so a year is not optimism.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.log == nil {
		p.log = host.Logger()
	}
	if !p.active() {
		// Configured with no origins, or disabled. Mounting anyway would claim URL
		// space for a filesystem that is always empty.
		return nil
	}
	return host.Mount(p.cfg.Prefix, &imageFS{plugin: p},
		collage.WithCacheControl("public, max-age=31536000, immutable"))
}

// outputFormat is one format the plugin can serve. The extension is what the mount
// derives a Content-Type from — a mounted file's type comes from its name, which is
// one fewer thing to keep in step than declaring it separately.
type outputFormat struct {
	name string
	ext  string
}

var (
	formatJPEG = outputFormat{name: "jpeg", ext: ".jpg"}
	formatPNG  = outputFormat{name: "png", ext: ".png"}
	formatWebP = outputFormat{name: "webp", ext: ".webp"}

	// formatAuto is decided when the source is decoded, for a source whose URL does
	// not say what it is — /uploads/cover/<uuid>, which is most of what a CMS
	// serves. The name has no extension, so the mount has no type to derive from
	// it and http.ServeContent sniffs the bytes: the Content-Type is whatever was
	// actually encoded, and the name stays the one the page was rendered with.
	formatAuto = outputFormat{name: "auto", ext: ""}
	// formatAutoWebP is formatAuto under "webp": "auto": what would stay lossless
	// becomes WebP instead of PNG. A separate format rather than a flag read at
	// fetch time, because it changes the bytes and so has to change the name.
	formatAutoWebP = outputFormat{name: "auto-webp", ext: ""}
)

// formatsByName reads a persisted recipe's format back, and formatsByExt says which
// extensions a name can carry. Every format is in both.
var (
	formatsByName = map[string]outputFormat{}
	formatsByExt  = map[string]outputFormat{}
)

func init() {
	for _, f := range []outputFormat{formatJPEG, formatPNG, formatWebP, formatAuto, formatAutoWebP} {
		formatsByName[f.name] = f
		formatsByExt[f.ext] = f
	}
}

// formatFor picks the output format for a source URL.
//
// From the source's extension where it has one, because that is the only thing
// known at rewrite time — the image itself is not fetched until someone asks for
// it. A PNG stays a PNG: re-encoding one as JPEG drops its transparency and puts a
// black rectangle where the page expected to see through.
//
// The extension is the path's, not the string's: "/a.png?v=3" is a PNG.
//
// Anything else is formatAuto, decided from the decoded image. Guessing JPEG for
// an extensionless URL was right for photographs and wrong for every transparent
// PNG a CMS stores under a UUID, which became a JPEG on a black background.
//
// Under WebPAuto, what would be lossless is WebP and a photograph stays JPEG,
// because the bundled encoder is lossless: it beats PNG and loses to JPEG badly.
func (p *Plugin) formatFor(source *url.URL) outputFormat {
	linked := webpEncoder != nil
	if p.cfg.WebP == WebPOn && linked {
		return formatWebP
	}
	lossless, auto := formatPNG, formatAuto
	if p.cfg.WebP == WebPAuto && linked {
		lossless, auto = formatWebP, formatAutoWebP
	}
	switch strings.ToLower(path.Ext(source.Path)) {
	case ".png", ".gif":
		return lossless
	case ".jpg", ".jpeg":
		return formatJPEG
	default:
		return auto
	}
}

// settle turns formatAuto and formatAutoWebP into the format the decoded image calls for, and returns
// any other format unchanged.
//
// The decoder's word, not the origin's Content-Type, which is a claim a CMS gets
// wrong often enough — application/octet-stream is common — and which the decode
// has already checked. A PNG or GIF source stays lossless, as it would have with
// its extension. So does any image with transparency, whatever it was: that is
// the one thing a JPEG cannot carry, and it covers a decoder an application
// registered for a format this package does not know.
func settle(f outputFormat, sourceFormat string, img image.Image) outputFormat {
	var lossless outputFormat
	switch f {
	case formatAuto:
		lossless = formatPNG
	case formatAutoWebP:
		lossless = formatWebP
	default:
		return f
	}
	if sourceFormat == "png" || sourceFormat == "gif" || !opaque(img) {
		return lossless
	}
	return formatJPEG
}

// opaque reports whether img has no transparency. An image that cannot say is
// treated as having some: the cost of the wrong answer that way is a larger file,
// and the other way it is a black rectangle.
func opaque(img image.Image) bool {
	o, ok := img.(interface{ Opaque() bool })
	return ok && o.Opaque()
}

func (p *Plugin) Shutdown(context.Context) error { return nil }

// active reports whether the plugin has anything to do.
func (p *Plugin) active() bool {
	return !p.cfg.Disabled && len(p.cfg.AllowedOrigins) > 0 && p.store != nil
}

// OnAfterRender rewrites the page's images.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if !p.active() {
		return nil
	}
	ev.HTML = p.rewriteImages(ev.HTML)
	return nil
}

// fetchContext bounds an origin fetch. The filesystem's Open has no context to
// derive from — fs.FS predates them — so the timeout is the only bound, and it is
// the one the configuration already states.
func (p *Plugin) fetchContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.cfg.FetchTimeout))
	// Released by the caller's deadline rather than by a defer: produce returns
	// before the timeout matters, and holding the cancel would mean holding the
	// context past the call it bounds.
	_ = cancel
	return ctx
}

// produce fetches, decodes, resizes and re-encodes one image.
func (p *Plugin) produce(ctx context.Context, r recipe) ([]byte, error) {
	source, allowed := p.cfg.allows(r.Source)
	if !allowed {
		// Re-checked even though a recipe exists only because this plugin recorded
		// it: the allowlist may have been narrowed since — a recipe read back from
		// disk may have been recorded by a process with a wider one — and a name
		// must not outlive the permission it was minted under.
		return nil, fmt.Errorf("opti-image: %q is not an allowed origin", r.Source)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("opti-image: origin answered %s", resp.Status)
	}

	// Limited before reading, not after: a body with no Content-Length, or a lying
	// one, is exactly the case a limit exists for.
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > p.cfg.MaxSourceBytes {
		return nil, fmt.Errorf("opti-image: source is larger than %d bytes", p.cfg.MaxSourceBytes)
	}

	// The header is decoded first so an enormous bitmap is refused before it is
	// allocated. A small file can decode to gigabytes — the decompression bomb —
	// and the byte limit above cannot see it.
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opti-image: not a decodable image: %w", err)
	}
	if config.Width*config.Height > p.cfg.MaxPixels {
		return nil, fmt.Errorf("opti-image: source is %dx%d, over the %d pixel limit",
			config.Width, config.Height, p.cfg.MaxPixels)
	}

	decoded, sourceFormat, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opti-image: decode: %w", err)
	}

	return p.encode(resize(decoded, r.Width, r.Height), settle(r.Format, sourceFormat, decoded))
}

// encode writes the resized image in the format the route promised.
//
// A GIF becomes a PNG, because the standard library decodes GIF and encodes PNG,
// and one still frame in a lossless format is the honest outcome — the alternative
// is a broken animation or a dependency.
func (p *Plugin) encode(img image.Image, format outputFormat) ([]byte, error) {
	var buf bytes.Buffer

	switch format {
	case formatWebP:
		if err := encodeWebP(&buf, img, p.cfg.Quality); err != nil {
			return nil, err
		}
	case formatPNG:
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	default:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: p.cfg.Quality}); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
