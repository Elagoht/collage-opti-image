package optiimage

import (
	"image"
	"io"

	"github.com/HugoSmits86/nativewebp"
)

// The encoder is linked unconditionally, and Config.WebP is the only switch.
//
// It used to be behind a "webp" build tag, on the reasoning that a plugin should not
// hand every application a dependency most of them will not use. That was the wrong
// trade twice over. The original reason for a tag was that every working WebP
// encoder needed cgo, and this one does not — it is pure Go, so nobody is being
// handed a toolchain requirement. And a system with two switches where one silently
// overrides the other is a system that tells you "webp": true and serves PNG; the
// warning that admitted it was a patch over a design that should not have needed
// one.
//
// WebP is also not an optional extra for an image optimiser. Gating it is close to
// gating JPEG.
//
// An application wanting a different encoder — a cgo binding to libwebp, for the
// lossy modes this one does not implement — calls RegisterWebPEncoder and replaces
// this one.
func init() { RegisterWebPEncoder(nativeWebP{}) }

// nativeWebP encodes lossless WebP in pure Go.
//
// Lossless is the whole of it, and it decides where this is worth turning on.
// Measured on a 760x428 diagonal gradient and on 760x428 of pixel noise, the latter
// standing in for a photograph because that is where lossless codecs struggle:
//
//	flat or synthetic     PNG 11,032   JPEG q82 7,720     WebP 1,624
//	photographic          PNG 976,984  JPEG q82 231,548   WebP 977,166
//
// So: five times smaller than JPEG on the first, four times larger on the second. A
// lossless codec cannot beat a lossy one on a photograph and does not try. That is
// what WebPAuto is for: WebP where it replaces a PNG, JPEG where it would replace
// one. WebPOn suits a site whose images are all illustrations, diagrams or
// interface captures.
//
// The advantage is also size-dependent, which is easy to miss when checking on a
// small fixture. At 200x150 the same gradient encodes to 492 bytes as PNG and 510
// as WebP — the container overhead is a fixed cost, and below a few kilobytes it is
// most of the file. Measure at the sizes the site actually serves.
type nativeWebP struct{}

// EncodeWebP writes img as lossless WebP.
//
// quality is ignored, and cannot be otherwise: there is no quality to trade when
// nothing is being discarded. It stays in the signature because the interface has
// to accommodate a lossy encoder too.
func (nativeWebP) EncodeWebP(w io.Writer, img image.Image, quality int) error {
	return nativewebp.Encode(w, img, nil)
}
