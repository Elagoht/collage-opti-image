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
	// WebP re-encodes to WebP where an encoder is available. The default build
	// has none — encoding WebP needs cgo — so this does nothing unless the binary
	// was built with the "webp" tag and an encoder registered. See webp.go.
	WebP bool `json:"webp"`
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
