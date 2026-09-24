package optiimage_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	optiimage "github.com/Elagoht/collage-opti-image"
)

// newCMS serves images the way most CMSs do: under a path with no extension, and
// with a Content-Type that says nothing useful. /uploads/logo is a PNG with a
// transparent half, /uploads/photo a JPEG.
func newCMS(t *testing.T) *httptest.Server {
	t.Helper()

	logo := image.NewNRGBA(image.Rect(0, 0, 400, 300))
	photo := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := range 300 {
		for x := range 400 {
			// The left half of the logo is fully transparent: that is what a
			// JPEG turns black.
			if x >= 200 {
				logo.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
			}
			photo.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var logoPNG, photoJPEG bytes.Buffer
	if err := png.Encode(&logoPNG, logo); err != nil {
		t.Fatalf("encode logo: %v", err)
	}
	if err := jpeg.Encode(&photoJPEG, photo, nil); err != nil {
		t.Fatalf("encode photo: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		switch r.URL.Path {
		case "/uploads/logo":
			w.Write(logoPNG.Bytes())
		case "/uploads/photo":
			w.Write(photoJPEG.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An extensionless source is decided from the decoded image, not guessed from the
// URL. Guessing JPEG turned every transparent PNG a CMS stores under a UUID into a
// JPEG on a black background.
func TestFormat_AnExtensionlessSourceKeepsItsTransparency(t *testing.T) {
	srv := newCMS(t)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/uploads/logo" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if ext := path.Ext(src); ext != "" {
		t.Errorf("src = %q has extension %q; the format is not known until the image is decoded", src, ext)
	}

	rec := get(t, site, src)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", src, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	decoded, format, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("served bytes are not an image: %v", err)
	}
	if format != "png" {
		t.Fatalf("served a %s, want png", format)
	}
	if _, _, _, a := decoded.At(10, 10).RGBA(); a != 0 {
		t.Errorf("alpha at a transparent pixel = %d, want 0", a)
	}

	// The second request is served from the store, and is still called a PNG.
	if ct := get(t, site, src).Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("cached Content-Type = %q, want image/png", ct)
	}
}

func TestFormat_AnExtensionlessPhotographIsAJPEG(t *testing.T) {
	srv := newCMS(t)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/uploads/photo" width="200" height="150">`)

	rec := get(t, site, srcOf(t, get(t, site, "/gallery").Body.String()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if _, format, err := image.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil || format != "jpeg" {
		t.Errorf("served %q (%v), want jpeg", format, err)
	}
}

// The extension is the path's, not the string's. "a.png?v=3" is a PNG, and the
// suffix check it replaced called it a JPEG.
func TestFormat_AQueryStringDoesNotHideTheExtension(t *testing.T) {
	_, srv := newOrigin(t, 400, 300)
	site := newSite(t, optiimage.Config{AllowedOrigins: allow(srv)},
		`<img src="`+srv.URL+`/a.png?v=3" width="200" height="150">`)

	src := srcOf(t, get(t, site, "/gallery").Body.String())
	if path.Ext(src) != ".png" {
		t.Errorf("src = %q, want a .png name", src)
	}
}
