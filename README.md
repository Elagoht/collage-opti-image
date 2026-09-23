# elagoht/opti-image

A collage plugin that rewrites images declaring a `width` and a `height` to resized
copies it serves itself.

```go
app, err := collage.New(&collage.Config{
	Plugins:      []collage.Plugin{optiimage.New()},
	PluginConfig: cfg,
})
```

It must be supplied through `Config.Plugins`: it registers the routes it serves
images from, and that happens while the application is built.

## Configuration

```json
{
  "elagoht/opti-image": {
    "allowedOrigins": [
      { "scheme": "https", "host": "images.example.com" }
    ],
    "prefix": "/_image/",
    "maxSourceBytes": 8388608,
    "maxPixels": 40000000,
    "fetchTimeout": "10s",
    "quality": 82,
    "webp": false
  }
}
```

**An empty `allowedOrigins` disables the plugin.** It never means "any host". A
fetcher that defaults to fetching anything is a server-side request forgery
primitive wearing a feature's name.

The scheme is part of an origin, not decoration: allowing a host without saying
which scheme permits a plaintext fetch of an image the page serves over TLS.

## Only declared-size images

An `<img>` is rewritten when it carries both `width` and `height` as pixel counts.
That is the contract, not a simplification:

- the declared size is the only statement of how large the image will be drawn, so
  without it any target size is a guess, and a guessed resize is worse than the
  original at every guess but one;
- the page already reserves the right space, so nothing reflows when the smaller
  file arrives;
- `width="100%"` is not a pixel count and is left alone.

`data-src` is left alone too — it contains `src=`, and rewriting it would break the
lazy-loading script the page uses while leaving the real source untouched.

## Nothing is fetched during the render

The render only rewrites the `src` and records what that name means. The fetch, the
decode and the resize happen the first time something opens it.

Doing the work during the render would make the first view of a page as slow as its
slowest image, while a reader waits on it. This way the page arrives at once and the
images arrive as the browser asks for them, which is the order the browser wanted
them in anyway. An origin that is down costs its image and not the page.

## The names, and why there is no signature

A name is a hash of what the image is — its source, its size, its format:

```
/_image/8f2a91c0b4e7d3a6.webp
```

Content-addressed, so two pages asking for the same picture at the same size share
one file and a name cannot come to mean different bytes. That is what makes
`immutable` and a one-year `max-age` honest rather than optimistic.

It also replaces an access-control problem instead of solving one. An earlier design
put the source URL in the path and signed it with an HMAC, because the endpoint would
otherwise fetch whatever it was handed — a server-side request forgery primitive
reachable by anyone who could read the page. **Here the request carries no source at
all.** It carries a filename, and a filename the plugin never minted stands for
nothing: `Open` looks it up, finds no recipe, and returns `fs.ErrNotExist`. There is
no signature because there is nothing to forge, and nothing to enumerate: a caller
cannot ask for a 9999×9999 resize because they cannot name one.

The recipes live in the process that rewrote the page. A fleet of servers behind a
shared page cache would hand one process's URLs to another, which would not know them
— so a multi-process deployment wants the pages built statically, or a shared store,
which this does not yet have.

## Static builds

A static build writes the images to disk, and the built site needs nothing running
behind it.

That is why the images are a mounted filesystem rather than a route. The builder
copies every mount into its output *after* every page has rendered — so by the time
it walks this one, the recipes name exactly the images the site uses. It writes real
files under the names the pages already link. A routed document could not be
enumerated that way: its path is dynamic, and a build has no way to guess what would
be asked for.

```
out/gallery/index.html            <img src="/_image/8f2a91c0b4e7d3a6.webp">
out/_image/8f2a91c0b4e7d3a6.webp  the file, 200x150
```

## Limits

`maxSourceBytes` bounds what is read. `maxPixels` is checked from the image header
*before* anything is decoded, because a small file can decode to an enormous bitmap
— the decompression bomb — and a byte limit cannot see it coming.

## Output format

Chosen from the source's extension, because the image is not fetched at rewrite
time. A PNG or GIF becomes a PNG; everything else becomes a JPEG. A PNG is not
re-encoded as JPEG: that drops its transparency and puts a black rectangle where the
page expected to see through.

Each format is its own route, because a collage document declares one content type
for every response it serves — so "which format" has to be part of which route.

Resizing is area averaging, standard library only. Nearest-neighbour downscaling is
where aliasing comes from: a one-pixel line either survives whole or vanishes, so a
photograph of a building acquires moiré and a screenshot of text becomes unreadable.
The averaging is done on premultiplied values, or a transparent black pixel beside
an opaque white one averages to translucent grey and a downscaled logo acquires a
dark halo.

## WebP

Off by default and behind a build tag:

```
go build -tags webp ./...
```

With the tag, a pure-Go lossless encoder is linked and registered. Without it the
package has no dependencies at all — which is what the tag is for. It is not a C
toolchain being gated, as it was when the only working encoders needed cgo; it is a
dependency most applications will not use.

Then turn it on in configuration:

```json
{ "elagoht/opti-image": { "webp": true } }
```

**The encoder is lossless, and that decides whether you want it.** Measured on a
760×428 diagonal gradient, and on 760×428 of pixel noise standing in for a
photograph:

| | PNG | JPEG q82 | WebP |
|---|---|---|---|
| flat or synthetic | 11,032 | 7,720 | **1,624** |
| photographic | 976,984 | **231,548** | 977,166 |

Five times smaller than JPEG on the first, four times larger on the second. A
lossless codec cannot beat a lossy one on a photograph and does not try. Turn it on
for a site whose images are illustrations, diagrams or interface captures; leave it
off for one whose images are photographs.

The advantage is size-dependent too, which is easy to miss when checking against a
small fixture: at 200×150 the same gradient is 492 bytes as PNG and 510 as WebP. The
container overhead is a fixed cost, and below a few kilobytes it is most of the file.
Measure at the sizes your site actually serves.

A binary built without the tag but configured with `"webp": true` serves the source
format and says so at startup:

```
WARN opti-image: WebP is configured but no encoder is linked; serving the source
     format instead  fix="build with -tags webp, or call optiimage.RegisterWebPEncoder"
```

Falling back is right — a missing encoder is a reason to serve PNG, not to refuse to
start — but doing it silently would leave you reading `"webp": true` in your
configuration and seeing PNG on the wire with nothing to connect the two.

An application wanting the lossy modes builds without the tag and calls
`RegisterWebPEncoder` with a cgo binding to libwebp. The interface takes a quality
argument for exactly that reason; the bundled encoder ignores it, because there is
no quality to trade when nothing is discarded.

## Clearing what it has produced

A mounted file never enters the framework's cache, so `InvalidateTags` cannot reach
these. The plugin holds them itself, bounded by `cacheBytes`, and purging is a
method:

```go
p := optiimage.New()
// ...
p.Purge()                  // every produced image
p.PurgeSource(imageURL)    // one origin image, at every size
```

Both drop the produced bytes and keep the recipes. A page already rendered links
these names, and forgetting what a name means would turn every one of those links
into a 404 rather than into a re-fetch.

The case it exists for is an origin that served a wrong file. The names are
content-addressed and cached for a year, so without this the only fix would be
restarting the process.
