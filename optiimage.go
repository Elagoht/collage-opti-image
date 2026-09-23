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
// an allowed origin has its src replaced with a signed URL under the plugin's own
// prefix. Nothing is fetched at that point.
//
// The fetch, the decode and the resize happen on the first request for that URL.
// Doing them during the render would make the first view of a page as slow as its
// slowest image, and would do it while holding a render the reader is waiting on.
// Doing them at the image endpoint means the page arrives at once and the images
// arrive as the browser asks for them, which is also the order the browser wants.
//
// # Why the URLs are signed
//
// The image endpoint takes a URL from the request and fetches it. Unsigned, that is
// a server-side request forgery primitive reachable by anyone who can read the
// page's HTML and edit a query string. The allowlist is checked again at fetch
// time, so the signature is not the only defence — it is the one that stops an
// attacker probing the allowlist at all, and stops the cache becoming unbounded
// storage keyed on strings they choose.
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
	"strings"

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

// Configure decodes the configuration and prepares the signer and the cache. It
// does not register the route: that needs Host, which Init receives.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	p.log = host.Logger()
	if err := host.Config(&p.cfg); err != nil {
		return err
	}
	p.cfg = p.cfg.withDefaults()
	if err := p.cfg.validate(); err != nil {
		return err
	}

	// Configured for WebP with nothing able to produce it. Serving the source
	// format is the right behaviour — a missing encoder is a reason to fall back,
	// not to refuse to start — but doing it silently would leave an operator
	// reading "webp": true in their configuration and PNG on the wire, with
	// nothing anywhere to connect the two.
	if p.cfg.WebP && webpEncoder == nil {
		p.log.Warn("opti-image: WebP is configured but no encoder is linked; serving the source format instead",
			"fix", "build with -tags webp, or call optiimage.RegisterWebPEncoder")
	}

	p.store = newStore(p.cfg.CacheBytes)
	p.client = &http.Client{Timeout: p.cfg.FetchTimeout}
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

// outputFormat is one format the plugin can serve.
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
)

// formatFor picks the output format for a source URL.
//
// From the source's extension, because that is the only thing known at rewrite
// time — the image itself is not fetched until someone asks for it. A PNG stays a
// PNG: re-encoding one as JPEG drops its transparency and puts a black rectangle
// where the page expected to see through.
func (p *Plugin) formatFor(source string) outputFormat {
	if p.cfg.WebP && webpEncoder != nil {
		return formatWebP
	}
	switch {
	case strings.HasSuffix(strings.ToLower(source), ".png"),
		strings.HasSuffix(strings.ToLower(source), ".gif"):
		return formatPNG
	default:
		return formatJPEG
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.FetchTimeout)
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
		// Re-checked even though the signature proves this plugin issued the URL:
		// the allowlist may have been narrowed since, and a signed URL must not
		// outlive the permission it was issued under.
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

	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opti-image: decode: %w", err)
	}

	return p.encode(resize(decoded, r.Width, r.Height), r.Format)
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
