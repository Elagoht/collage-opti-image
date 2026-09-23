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

The render only rewrites the URL. The fetch, the decode and the resize happen on the
first request for that URL.

Doing the work during the render would make the first view of a page as slow as its
slowest image, while a reader waits on it. At the image endpoint the page arrives at
once and the images arrive as the browser asks for them — which is the order the
browser wanted them in anyway. An origin that is down costs its image and not the
page.

## Why the URLs are signed

The image endpoint takes a source from the request and fetches it. The allowlist is
re-checked at that point, so a token naming some other host is refused whether or
not it is signed.

What the HMAC alone prevents is a *valid* token naming an allowed origin at any size
the requester likes. Without it, anyone who can read the page can request a
9999×9999 resize of every image on the site, as many distinct sizes as they care to
type — each one a fetch, a decode, a resize and a cache entry. That is a denial of
service built entirely out of legitimate-looking requests, and
`TestPlugin_SignatureStopsForgedWorkAgainstAnAllowedOrigin` is what notices when the
check goes away.

The key is generated per process. A restart invalidates outstanding URLs, which
costs a re-render of pages still held downstream and buys a key nobody has to store,
rotate or keep out of a configuration file. **A fleet behind a load balancer wants a
shared key**, and `Config` would have to grow one before this is deployed that way.

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

## Caching

The images are documents with a 30-day `Incremental` strategy, so the framework
caches the bytes, answers conditional requests and sends a real `max-age`. The URL
names the source and the size and is signed, so its content cannot change without
the URL changing — which is what makes a long life correct rather than convenient.

They share the application's page cache. A site with many images should say so in
`Cache.MaxEntries`.
