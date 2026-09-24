package optiimage

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/opti-image"

// Origin is one host the plugin is allowed to fetch from.
//
// Scheme is part of it, not an afterthought: allowing "example.com" without saying
// which scheme allows a plaintext fetch of an image the page will serve over TLS,
// which is a mixed-content warning at best and a tampering opportunity at worst.
type Origin struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
}

// Config is the plugin's configuration.
type Config struct {
	// AllowedOrigins is the set of hosts images may be fetched from. An empty list
	// disables the plugin entirely rather than allowing everything: a fetcher that
	// defaults to "any host" is an SSRF primitive wearing a feature's name.
	AllowedOrigins []Origin `json:"allowedOrigins"`
	// Prefix is the URL prefix optimised images are served under. It must begin
	// and end with "/". Defaults to "/_image/".
	Prefix string `json:"prefix"`
	// MaxSourceBytes refuses an origin image larger than this. Defaults to 8 MiB.
	// Without it, one hostile or careless origin can exhaust the process's memory.
	MaxSourceBytes int64 `json:"maxSourceBytes"`
	// MaxPixels refuses an origin image with more pixels than this, before it is
	// decoded. Defaults to 40 million. A small file can decode to an enormous
	// bitmap — the decompression bomb — and the byte limit above does not see it.
	MaxPixels int `json:"maxPixels"`
	// FetchTimeout bounds a single origin fetch. Defaults to 10s.
	//
	// In JSON it is written the way a person writes a duration — "10s", "500ms" —
	// as well as as a number of nanoseconds. See Duration.
	FetchTimeout Duration `json:"fetchTimeout"`
	// CacheBytes caps the memory held by resized images. Defaults to 64 MiB.
	CacheBytes int64 `json:"cacheBytes"`
	// CacheDir is where produced images are kept between restarts. An empty value
	// means ".cache/opti-image", relative to the working directory.
	//
	// A dotted directory in the project, the way a build tool does it — findable,
	// one line in .gitignore, and gone when you delete it. The temporary directory
	// would keep it out of sight, which is the problem rather than the point: on
	// macOS that is /var/folders/xy/…/T, and a cache nobody can find is a cache
	// nobody can clear.
	CacheDir string `json:"cacheDir"`
	// NoDiskCache keeps produced images in memory only, so nothing is written
	// anywhere. A read-only deployment does not need this — a directory that cannot
	// be created or written to disables the disk cache by itself, with one line in
	// the log — but a deployment that would rather not write at all can say so.
	NoDiskCache bool `json:"noDiskCache"`
	// Quality is the JPEG quality of re-encoded images, 1..100. Defaults to 82.
	Quality int `json:"quality"`
	// WebP says which images are re-encoded to WebP: none, all, or — "auto" — only
	// those that would otherwise be lossless. See WebPMode.
	WebP WebPMode `json:"webp"`
	// AlphaThreshold is how much of an image may be visibly transparent while it
	// still counts as opaque when its format is decided from the image — for a
	// source without an extension. A share of the pixels, 0 to 1. Defaults to 0.
	//
	// A pixel at least 98% opaque is never counted: that is edge anti-aliasing a
	// reader cannot see, and one of them used to be enough to send a photograph to
	// a lossless format at several times its size. Raise this to also let a few
	// genuinely transparent pixels go — 0.001 is a thousandth of the image — and
	// keep it low: a photograph with rounded, transparent corners that is judged
	// opaque becomes a JPEG with square ones.
	AlphaThreshold float64 `json:"alphaThreshold"`
	// Disabled turns the plugin off without removing it from the application.
	Disabled bool `json:"disabled"`
}

// withDefaults returns cfg with every unset field filled in.
func (c Config) withDefaults() Config {
	if c.Prefix == "" {
		c.Prefix = "/_image/"
	}
	if c.MaxSourceBytes <= 0 {
		c.MaxSourceBytes = 8 << 20
	}
	if c.MaxPixels <= 0 {
		c.MaxPixels = 40_000_000
	}
	if c.FetchTimeout <= 0 {
		c.FetchTimeout = Duration(10 * time.Second)
	}
	if c.CacheBytes <= 0 {
		c.CacheBytes = 64 << 20
	}
	if c.CacheDir == "" {
		c.CacheDir = filepath.Join(".cache", "opti-image")
	}
	if c.Quality <= 0 || c.Quality > 100 {
		c.Quality = 82
	}
	return c
}

// validate reports a configuration that cannot work.
func (c Config) validate() error {
	if !strings.HasPrefix(c.Prefix, "/") || !strings.HasSuffix(c.Prefix, "/") {
		return fmt.Errorf("opti-image: prefix %q must begin and end with \"/\"", c.Prefix)
	}
	if c.Prefix == "/" {
		return fmt.Errorf("opti-image: prefix must not be \"/\", which would claim the whole site")
	}
	if c.AlphaThreshold < 0 || c.AlphaThreshold >= 1 {
		return fmt.Errorf("opti-image: alphaThreshold %v must be at least 0 and below 1", c.AlphaThreshold)
	}
	for _, origin := range c.AllowedOrigins {
		if origin.Host == "" {
			return fmt.Errorf("opti-image: an allowed origin has no host")
		}
		if origin.Scheme != "http" && origin.Scheme != "https" {
			return fmt.Errorf("opti-image: origin %q has scheme %q, want http or https", origin.Host, origin.Scheme)
		}
	}
	return nil
}

// allows reports whether raw names an image this plugin may fetch.
//
// The comparison is on the parsed URL's scheme and host, never on the string: a
// string check is defeated by "https://allowed.example@evil.example/x", whose host
// is evil.example and whose prefix is the allowed one. The port is part of the host
// for this purpose, so allowing "images.example.com" does not allow
// "images.example.com:8080" — a different service on a different port is a
// different origin, whatever the certificate says.
func (c Config) allows(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, false
	}
	for _, origin := range c.AllowedOrigins {
		if parsed.Scheme == origin.Scheme && parsed.Host == origin.Host {
			return parsed, true
		}
	}
	return nil, false
}

// Duration is a time.Duration that reads "10s" from JSON as well as a number.
//
// encoding/json unmarshals a time.Duration from a number of nanoseconds and from
// nothing else, so a configuration file written by a person — which is what a
// plugin configuration file is — fails on the only spelling that person was ever
// going to use. "fetchTimeout": "10s" is what everybody writes; 10000000000 is what
// nobody does.
//
// The number is still accepted, because a file written by a program is a file
// somewhere, and refusing it would break it for the sake of tidiness.
type Duration time.Duration

// UnmarshalJSON accepts "10s" and 10000000000 alike.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var asNumber int64
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*d = Duration(asNumber)
		return nil
	}

	var asText string
	if err := json.Unmarshal(data, &asText); err != nil {
		return fmt.Errorf("opti-image: fetchTimeout must be a duration such as \"10s\", or nanoseconds: %w", err)
	}
	parsed, err := time.ParseDuration(asText)
	if err != nil {
		return fmt.Errorf("opti-image: fetchTimeout %q: %w", asText, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalJSON writes the form a person reads, so a configuration round-trips into
// something still worth editing.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// WebPMode says which images are re-encoded as WebP.
//
// Three values rather than a switch, because the bundled encoder is lossless and
// that splits a site's images in two. Measured on a real blog: a 640x360 card was
// 28–42 KB as JPEG q82 and 155–206 KB as WebP, a 1280x720 cover 91–101 KB against
// 250–352 KB — while a 256x256 avatar with a PNG source was 80 KB as PNG and 63 KB
// as WebP. "On" is right for a site of diagrams and wrong for a site of photographs,
// and most sites are both.
//
// In JSON it is false, true or "auto". The booleans are what the switch used to be,
// and a configuration written for it still reads the same.
type WebPMode uint8

const (
	// WebPOff never encodes WebP. It is the zero value.
	WebPOff WebPMode = iota
	// WebPOn encodes every image as WebP.
	WebPOn
	// WebPAuto encodes as WebP only what would otherwise be lossless — PNG and GIF
	// sources, and images with transparency — and leaves photographs as JPEG.
	WebPAuto
)

// UnmarshalJSON accepts false, true and "auto".
func (m *WebPMode) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		if asBool {
			*m = WebPOn
		} else {
			*m = WebPOff
		}
		return nil
	}

	var asText string
	if err := json.Unmarshal(data, &asText); err != nil || asText != "auto" {
		return fmt.Errorf("opti-image: webp must be true, false or \"auto\", not %s", data)
	}
	*m = WebPAuto
	return nil
}

// MarshalJSON writes the spelling UnmarshalJSON reads.
func (m WebPMode) MarshalJSON() ([]byte, error) {
	switch m {
	case WebPOn:
		return []byte("true"), nil
	case WebPAuto:
		return []byte(`"auto"`), nil
	default:
		return []byte("false"), nil
	}
}
