package optiimage_test

import (
	"bytes"
	"encoding/json"
	"image"
	"net/http"
	"path"
	"strings"
	"testing"

	// The decoder, to check what the encoder produced. It is a test dependency
	// only: the plugin encodes WebP and never reads it.
	_ "golang.org/x/image/webp"

	optiimage "github.com/Elagoht/collage-opti-image"
)

func TestWebP_IsServedWhenEnabledAndLinked(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv), WebP: optiimage.WebPOn},
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

// Under "auto" only what would be lossless becomes WebP. The bundled encoder is
// lossless, and against JPEG on a photograph it loses by a factor of four or more.
func TestWebP_AutoConvertsOnlyLosslessSources(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	cms := newCMS(t)
	origins := append(allow(srv), allow(cms)...)

	for _, tc := range []struct {
		src, wantExt, wantType string
	}{
		{srv.URL + "/a.png", ".webp", "image/webp"},
		{srv.URL + "/a.gif", ".webp", "image/webp"},
		{srv.URL + "/a.jpg", ".jpg", "image/jpeg"},
		{cms.URL + "/uploads/logo", "", "image/webp"},
		{cms.URL + "/uploads/photo", "", "image/jpeg"},
	} {
		site := newSite(t, optiimage.Config{AllowedOrigins: origins, WebP: optiimage.WebPAuto},
			`<img src="`+tc.src+`" width="200" height="150">`)

		name := srcOf(t, get(t, site, "/gallery").Body.String())
		if ext := path.Ext(name); ext != tc.wantExt {
			t.Errorf("%s: name %q, want extension %q", tc.src, name, tc.wantExt)
		}
		rec := get(t, site, name)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: GET %s = %d", tc.src, name, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != tc.wantType {
			t.Errorf("%s: Content-Type = %q, want %q", tc.src, ct, tc.wantType)
		}
	}
}

// "auto" changes what an extensionless source is encoded as, so it has to change
// its name: a name is content-addressed and cached for a year, and switching the
// mode must not serve different bytes under one.
func TestWebP_AutoChangesTheNameOfAnExtensionlessSource(t *testing.T) {
	cms := newCMS(t)
	markup := `<img src="` + cms.URL + `/uploads/logo" width="200" height="150">`

	off := srcOf(t, get(t, newSite(t, optiimage.Config{AllowedOrigins: allow(cms)}, markup), "/gallery").Body.String())
	auto := srcOf(t, get(t, newSite(t, optiimage.Config{AllowedOrigins: allow(cms), WebP: optiimage.WebPAuto}, markup), "/gallery").Body.String())
	if off == auto {
		t.Errorf("both modes named the image %q", off)
	}
}

// keep the json import honest across both build configurations
var _ = json.Marshal
