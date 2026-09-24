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
    "webp": "auto"
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

The `src` is read as what it is — an HTML attribute value — and decoded before it is
treated as a URL. `html/template` writes a `+` in an attribute as `&#43;`, so a path
such as `/covers/python-examples+1746296252694` arrives encoded; taken verbatim, the
origin would be asked for `&` followed by a fragment. The rewritten value is escaped
on the way back.

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

The recipes live in the process that rewrote the page, and are written beside the
images as `<name>.json` so they outlive it. collage's disk cache keeps rendered pages
across a restart; without the recipe on disk, a cached page's image that nobody had
requested yet was a 404 until the page was rendered again.

That does not reopen what the names closed. The names are a plain hash rather than
an HMAC on purpose: nothing rests on a name being hard to compute, only on a name
resolving to a recipe this plugin recorded. A recipe read back from disk is accepted
only if it hashes to the name it is stored under, and the allowlist is checked again
before anything is fetched, so narrowing it takes effect on recipes written under a
wider one. Only names of the shape the plugin mints are looked up at all, so a
request cannot reach a recipe file, or a temporary one, as if it were an image.

A key would add nothing here. The cache directory is already trusted — the plugin
serves the image bytes in it without checking them — so whoever can write a recipe
there can write the image instead, and a key kept beside them would be no secret.

A fleet of servers behind a shared page cache would hand one process's URLs to
another: they know each other's names when they share `cacheDir`, and not otherwise
— so a multi-process deployment wants a shared cache directory or the pages built
statically.

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

Chosen from the source's extension where it has one, because the image is not
fetched at rewrite time. A `.png` or `.gif` becomes a PNG and a `.jpg` or `.jpeg` a
JPEG. A PNG is not re-encoded as JPEG: that drops its transparency and puts a black
rectangle where the page expected to see through. The extension is the path's, so
`/a.png?v=3` is a PNG.

A source whose URL does not say — `/uploads/cover/<uuid>`, which is most of what a
CMS serves — is decided when it is decoded. A PNG or GIF, or any image with
transparency, stays lossless; anything else becomes a JPEG. The decoder decides, not
the origin's `Content-Type`, which is a claim, and which a CMS often sends as
`application/octet-stream`.

Its name has no extension, because the format is not known when the page is
rendered and the name cannot change after it:

```
/_image/8f2a91c0b4e7d3a6e1c9b8a7f6d5e4c3
```

A mounted file's type comes from its extension, so with none `http.ServeContent`
sniffs the bytes, and the `Content-Type` is whatever was actually encoded. A static
build writes the file without an extension too; a static host will usually send it
as `application/octet-stream`, which browsers display in an `<img>` all the same.

Resizing is area averaging, standard library only. Nearest-neighbour downscaling is
where aliasing comes from: a one-pixel line either survives whole or vanishes, so a
photograph of a building acquires moiré and a screenshot of text becomes unreadable.
The averaging is done on premultiplied values, or a transparent black pixel beside
an opaque white one averages to translucent grey and a downscaled logo acquires a
dark halo.

## WebP

Three values:

```json
{ "elagoht/opti-image": { "webp": "auto" } }
```

- `false`, the default: no WebP.
- `true`: every image is WebP.
- `"auto"`: only what would otherwise be lossless — PNG and GIF sources, and images
  with transparency — is WebP; photographs stay JPEG. This is the one to use on a
  site that has both, which is most of them.

`true` and `false` are what the setting used to be, and a configuration written for
them reads the same. In Go the field is a `WebPMode`: `optiimage.WebPOff`,
`optiimage.WebPOn`, `optiimage.WebPAuto`.

The encoder is linked unconditionally and is pure Go. It used to be behind a `webp`
build tag, which was the wrong trade twice over: the reason for a tag was that every
working WebP encoder needed cgo, and this one does not; and two switches where one
silently overrides the other is a system that reads `"webp": true` and serves PNG.
WebP is not an optional extra for an image optimiser anyway — gating it is close to
gating JPEG.

**The encoder is lossless, and that decides whether you want it.** Measured on a
760×428 diagonal gradient, and on 760×428 of pixel noise standing in for a
photograph:

| | PNG | JPEG q82 | WebP |
|---|---|---|---|
| flat or synthetic | 11,032 | 7,720 | **1,624** |
| photographic | 976,984 | **231,548** | 977,166 |

Five times smaller than JPEG on the first, four times larger on the second. A
lossless codec cannot beat a lossy one on a photograph and does not try. The same
shows on a real blog:

| | JPEG q82 | PNG | WebP |
|---|---|---|---|
| card, 640×360 photograph | **28–42 KB** | | 155–206 KB |
| cover, 1280×720 photograph | **91–101 KB** | | 250–352 KB |
| avatar, 256×256, PNG source | | 80 KB | **63 KB** |

Which is what `"auto"` does: WebP where it replaces a PNG, JPEG where it would
replace one. `true` is for a site whose images are all illustrations, diagrams or
interface captures.

The advantage is size-dependent too, which is easy to miss when checking against a
small fixture: at 200×150 the same gradient is 492 bytes as PNG and 510 as WebP. The
container overhead is a fixed cost, and below a few kilobytes it is most of the file.
Measure at the sizes your site actually serves.

An application wanting the lossy modes calls `RegisterWebPEncoder` with a cgo
binding to libwebp, replacing the bundled one. The interface takes a quality
argument for exactly that reason; the bundled encoder ignores it, because there is
no quality to trade when nothing is discarded.

## Where the images live

Produced images are held in memory, bounded by `cacheBytes`, and written to a
directory so they survive a restart:

```json
{ "elagoht/opti-image": { "cacheDir": "/var/cache/opti-image" } }
```

The default is `.cache/opti-image`, relative to the working directory — a dotted
directory in the project, the way a build tool does it. Findable, one line in
`.gitignore`, and gone when you delete it. A temporary directory would keep it out of
sight, which is the problem rather than the point: on macOS that is
`/var/folders/xy/…/T`, and a cache nobody can find is a cache nobody can clear.

This works at all because the names are content-addressed. A file called
`8f2a91c0b4e7d3a6.webp` holds one thing and always will, so a restarted process can
use what an earlier one produced without validating it and without an expiry. A cache
keyed on a location would need both.

A directory that cannot be created or written to disables the disk cache, once, with
a line in the log — a read-only deployment is a normal deployment, and a plugin that
refuses to start on one, or that retries the same failing write for every image, is
worse than one that keeps everything in memory. `"noDiskCache": true` says the same
thing on purpose rather than by accident.

Writes go through a temporary file and a rename, so a reader never sees a
half-written image. Two processes producing the same image concurrently is normal —
they agree on the name, because the name is the content.

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

Both drop the produced bytes, remove the image files — an earlier process's too,
found through the recipes it left — and keep the recipes. A page already rendered links
these names, and forgetting what a name means would turn every one of those links
into a 404 rather than into a re-fetch.

The case it exists for is an origin that served a wrong file. The names are
content-addressed and cached for a year, so without this the only fix would be
restarting the process.
