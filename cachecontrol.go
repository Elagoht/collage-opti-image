package optiimage

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// immutableCacheControl is what an image of the site's own Files is served with: a
// year, the longest a client is required to honour, and immutable, since the
// file's content is in the image's name.
const immutableCacheControl = "public, max-age=31536000, immutable"

// originCacheControl is what an image fetched from an origin is served with, and
// the mount's own header: OriginMaxAge, and nothing promised beyond it.
func (p *Plugin) originCacheControl() string {
	return "public, max-age=" + strconv.FormatInt(int64(time.Duration(p.cfg.OriginMaxAge)/time.Second), 10)
}

// cacheHeaders gives an image of the site's own Files the year-long, immutable
// Cache-Control the mount's shorter one cannot.
//
// A middleware rather than a second mount, because both kinds of image share one
// prefix and their names say nothing a request could route on: which kind a name
// is, only its recipe knows. It upgrades a header the mount already set, and only
// that one: in development the mount sends no-store, and that is left alone.
func (p *Plugin) cacheHeaders(next http.Handler) http.Handler {
	origin := p.originCacheControl()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, p.cfg.Prefix)
		if !ok || !validName(name) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&cacheWriter{ResponseWriter: w, plugin: p, name: name, origin: origin}, r)
	})
}

// cacheWriter decides the Cache-Control of one image when its status is written.
type cacheWriter struct {
	http.ResponseWriter
	plugin *Plugin
	name   string
	origin string
	done   bool
}

func (w *cacheWriter) WriteHeader(status int) {
	if !w.done && status >= http.StatusOK {
		w.done = true
		w.decide(status)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *cacheWriter) Write(b []byte) (int, error) {
	if !w.done {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (w *cacheWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// decide upgrades the header to immutable for a successful response of an image
// whose recipe carries its source's digest. An error, an unknown name, or an
// image from an origin keeps what the mount set.
func (w *cacheWriter) decide(status int) {
	switch status {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
	default:
		return
	}
	h := w.Header()
	if h.Get("Cache-Control") != w.origin {
		return
	}
	if r, ok := w.plugin.store.lookup(w.name); ok && r.Digest != "" {
		h.Set("Cache-Control", immutableCacheControl)
	}
}
