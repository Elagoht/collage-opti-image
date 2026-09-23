package optiimage

import "bytes"

// readSeekCloser is a *bytes.Reader that can be closed, which is all an fs.File
// needs on top of one.
type readSeekCloser struct{ *bytes.Reader }

func newReadSeekCloser(b []byte) *readSeekCloser {
	return &readSeekCloser{Reader: bytes.NewReader(b)}
}

func (*readSeekCloser) Close() error { return nil }
