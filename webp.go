package optiimage

import (
	"errors"
	"image"
	"io"
)

// errNoWebPEncoder is returned when WebP output is asked for and nothing can
// produce it.
var errNoWebPEncoder = errors.New("opti-image: no WebP encoder is linked")

// WebPEncoder encodes an image as WebP.
//
// A pure-Go lossless encoder is linked unconditionally and registered at init — see
// webp_encoder.go — so no build tag and no cgo are needed for WebP. The interface
// exists so it can be replaced: an application wanting the lossy modes registers a
// cgo binding to libwebp instead.
//
//	func init() { optiimage.RegisterWebPEncoder(myCgoEncoder{}) }
type WebPEncoder interface {
	// EncodeWebP writes img to w at the given quality, 1..100.
	EncodeWebP(w io.Writer, img image.Image, quality int) error
}

// webpEncoder is what RegisterWebPEncoder set, or nil.
var webpEncoder WebPEncoder

// RegisterWebPEncoder installs the encoder used when Config.WebP asks for WebP,
// replacing the bundled one. It is intended for an init function, and is not safe
// to call once an application is serving.
func RegisterWebPEncoder(enc WebPEncoder) { webpEncoder = enc }

// encodeWebP encodes img, or reports that nothing can.
func encodeWebP(w io.Writer, img image.Image, quality int) error {
	if webpEncoder == nil {
		return errNoWebPEncoder
	}
	return webpEncoder.EncodeWebP(w, img, quality)
}
