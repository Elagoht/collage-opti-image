//go:build !webp

package optiimage_test

// webpLinked reports whether this build carries a WebP encoder.
//
// It lives in the test package rather than beside the encoder because the only
// thing that needs it is a test asserting opposite behaviour in the two builds,
// and exporting a constant from the package to say so would widen its surface for
// nobody else's benefit.
const webpLinked = false
