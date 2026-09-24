package optiimage

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

// photo is a 100×100 opaque image with n pixels set to alpha a — a photograph a
// CMS stored with an alpha channel, and the few pixels along an edge it touched.
func photo(n int, a uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for i := range n {
		img.SetNRGBA(i%100, i/100, color.NRGBA{R: 200, G: 40, B: 40, A: a})
	}
	return img
}

// A handful of nearly opaque pixels do not make a photograph transparent: it
// stays a JPEG rather than going lossless at several times the size.
func TestSettle_NearlyOpaqueIsAPhotograph(t *testing.T) {
	for _, auto := range []outputFormat{formatAuto, formatAutoWebP} {
		if got := settle(auto, "webp", photo(72, 254), 0); got != formatJPEG {
			t.Errorf("%s with 72 pixels at 254: %s, want jpeg", auto.name, got.name)
		}
	}
}

// Transparency a reader can see still keeps an image lossless — unless the
// threshold allows that much of it.
func TestSettle_VisibleTransparency(t *testing.T) {
	corners := photo(5, 0) // five fully transparent pixels in ten thousand
	if got := settle(formatAuto, "webp", corners, 0); got != formatPNG {
		t.Errorf("default: %s, want png: those pixels can be seen through", got.name)
	}
	if got := settle(formatAuto, "webp", corners, 0.001); got != formatJPEG {
		t.Errorf("threshold 0.001: %s, want jpeg: five pixels are within a thousandth", got.name)
	}
	if got := settle(formatAuto, "webp", photo(20, 0), 0.001); got != formatPNG {
		t.Errorf("twenty transparent pixels at 0.001: %s, want png", got.name)
	}
	if got := settle(formatAuto, "png", photo(0, 0xff), 0); got != formatPNG {
		t.Errorf("a PNG source: %s, want png whatever its pixels", got.name)
	}
}

// Encoded as JPEG, a translucent pixel keeps its colour instead of being darkened
// toward black by its premultiplied value.
func TestEncode_JPEGShowsTranslucentPixelsInTheirOwnColour(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(img.Pix); i += 4 {
		copy(img.Pix[i:], []byte{200, 40, 40, 128})
	}
	out, err := (&Plugin{cfg: Config{Quality: 90}}).encode(img, formatJPEG)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r, _, _, _ := decoded.At(8, 8).RGBA()
	if r>>8 < 180 {
		t.Errorf("red = %d, want about 200: the pixel was darkened by its alpha", r>>8)
	}
}

// The threshold is part of an auto format's name, so changing it makes new images
// rather than new bytes under old names; a fixed format's name does not move.
func TestRecipe_TheThresholdIsInAnAutoName(t *testing.T) {
	auto := recipe{Source: "https://ok.example/x", Width: 10, Height: 10, Format: formatAuto}
	raised := auto
	raised.Alpha = 0.001
	if auto.name() == raised.name() {
		t.Error("an auto recipe's name ignores its threshold")
	}
	jpg := recipe{Source: "https://ok.example/x.jpg", Width: 10, Height: 10, Format: formatJPEG}
	jpgRaised := jpg
	jpgRaised.Alpha = 0.001
	if jpg.name() != jpgRaised.name() {
		t.Error("a JPEG recipe's name changed with a threshold that cannot affect it")
	}

	persisted, err := raised.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if back, ok := unmarshalRecipe(raised.name(), persisted); !ok || back.Alpha != 0.001 {
		t.Errorf("round trip = %+v, %v; want the threshold kept", back, ok)
	}
}

func TestConfig_AlphaThresholdIsAShare(t *testing.T) {
	for _, bad := range []float64{-0.1, 1, 2} {
		c := Config{AlphaThreshold: bad}.withDefaults()
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), "alphaThreshold") {
			t.Errorf("AlphaThreshold %v: validate() = %v, want it refused", bad, err)
		}
	}
}
