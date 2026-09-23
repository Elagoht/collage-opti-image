//go:build webp

package optiimage

import (
	"image"
	"io"

	"github.com/HugoSmits86/nativewebp"
)

// This file is what the "webp" build tag turns on. Without it the package has no
// dependencies at all, and that is why the tag exists — not because the encoder
// needs a C toolchain (this one does not), but because a plugin should not hand
// every application a dependency most of them will not use.
//
//	go build -tags webp ./...
//
// An application that wants a different encoder — a cgo binding to libwebp, for the
// lossy modes this one does not implement — builds without the tag and calls
// RegisterWebPEncoder itself.
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
// lossless codec cannot beat a lossy one on a photograph and does not try. Turn WebP
// on for a site whose images are illustrations, diagrams or interface captures;
// leave it off for one whose images are photographs.
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
