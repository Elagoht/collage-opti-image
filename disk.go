package optiimage

import (
	"os"
	"path/filepath"
	"sync"
)

// The disk half of the store.
//
// It works because the names are content-addressed: a file called
// 8f2a91c0b4e7d3a6.webp holds one thing and always will, so a restarted process can
// pick up what an earlier one produced without checking whether it is still
// correct. That is the whole reason a disk cache is worth having here and would not
// be if the names described a location instead.

// disk writes and reads produced images under a directory.
//
// A directory it cannot create or write to disables it, once, with a line in the
// log. A read-only deployment is a normal deployment, and a plugin that refuses to
// start on one — or that retries the same failing write for every image — is worse
// than one that quietly keeps everything in memory.
type disk struct {
	dir string

	once     sync.Once
	usable   bool
	disabled func(error)
}

func newDisk(dir string, onDisabled func(error)) *disk {
	return &disk{dir: dir, disabled: onDisabled}
}

// ready prepares the directory on first use and reports whether it can be used.
func (d *disk) ready() bool {
	d.once.Do(func() {
		if err := os.MkdirAll(d.dir, 0o755); err != nil {
			d.disabled(err)
			return
		}
		// Created is not the same as writable — a directory can exist and belong to
		// somebody else — so the check is a write rather than a stat.
		probe, err := os.CreateTemp(d.dir, ".probe-*")
		if err != nil {
			d.disabled(err)
			return
		}
		probe.Close()
		os.Remove(probe.Name())
		d.usable = true
	})
	return d.usable
}

// read returns a previously produced image.
func (d *disk) read(name string) ([]byte, bool) {
	if !d.ready() {
		return nil, false
	}
	body, err := os.ReadFile(d.path(name))
	if err != nil {
		return nil, false
	}
	return body, true
}

// write stores a produced image.
//
// Through a temporary file and a rename, so a reader never sees a half-written
// image: two processes producing the same image concurrently is normal — they
// agree on the name, because the name is the content — and without the rename one
// would be reading what the other is still writing.
func (d *disk) write(name string, body []byte) error {
	if !d.ready() {
		return nil
	}

	tmp, err := os.CreateTemp(d.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, d.path(name)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// remove deletes a stored image, if it is there.
func (d *disk) remove(name string) {
	if !d.ready() {
		return
	}
	os.Remove(d.path(name))
}

// path is the file a name is stored at.
//
// filepath.Base is belt and braces: a name is a hex hash plus an extension, built
// by this package and never taken from a request. But this is the one place a name
// becomes a filesystem path, so it is the one place worth refusing anything that
// looks like a directory.
func (d *disk) path(name string) string {
	return filepath.Join(d.dir, filepath.Base(name))
}
