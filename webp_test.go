package optiimage_test

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"strings"
	"testing"

	// The decoder, to check what the encoder produced. It is a test dependency
	// only: the plugin encodes WebP and never reads it.
	_ "golang.org/x/image/webp"

	optiimage "github.com/Elagoht/collage-opti-image"
)

func TestWebP_IsServedWhenEnabledAndLinked(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv), WebP: true},
		`<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if !strings.HasSuffix(src, ".webp") {
		t.Fatalf("src = %q, want a .webp name — the encoder is linked in this build", src)
	}

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", src, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q, want image/webp", ct)
	}

	decoded, format, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("the body is not a decodable image: %v", err)
	}
	if format != "webp" {
		t.Errorf("format = %q, want webp — the route promised it", format)
	}
	if got := decoded.Bounds().Size(); got.X != 200 || got.Y != 150 {
		t.Errorf("image is %v, want 200x150", got)
	}
}

func TestWebP_ConfiguredOffStillServesTheSourceFormat(t *testing.T) {
	// Linking an encoder must not force its use. A site whose images are
	// photographs wants JPEG, and says so by leaving WebP false.
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png" width="200" height="150">`)

	if src := srcOf(t, get(t, site, "/gallery").Body.String()); !strings.HasSuffix(src, ".png") {
		t.Errorf("src = %q, want a .png name", src)
	}
}

// keep the json import honest across both build configurations
var _ = json.Marshal
