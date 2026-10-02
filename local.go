package optiimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"sync"
)

// digests remembers each local file's content hash, so a render hashes a file
// once per process rather than once per page. Not in development, where the file
// is the one being edited.
type digests struct {
	mu sync.Mutex
	// byPath is keyed by the URL path, which names one file of one filesystem:
	// an fs.FS itself may be a map, which cannot be a key.
	byPath map[string]string
}

// digest returns the hex SHA-256 of file in fsys, the file at the URL path key,
// refusing one over MaxSourceBytes as a fetched source is refused.
func (p *Plugin) digest(key string, fsys fs.FS, file string) (string, error) {
	if !p.devMode {
		p.digests.mu.Lock()
		d, ok := p.digests.byPath[key]
		p.digests.mu.Unlock()
		if ok {
			return d, nil
		}
	}
	body, err := p.readLocal(fsys, file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	d := hex.EncodeToString(sum[:])
	if !p.devMode {
		p.digests.mu.Lock()
		if p.digests.byPath == nil {
			p.digests.byPath = map[string]string{}
		}
		p.digests.byPath[key] = d
		p.digests.mu.Unlock()
	}
	return d, nil
}

// readLocal reads a local source, limited as an origin's body is: before reading,
// so a file larger than the limit is never read whole.
func (p *Plugin) readLocal(fsys fs.FS, file string) ([]byte, error) {
	f, err := fsys.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, err
	} else if info.IsDir() {
		return nil, fmt.Errorf("opti-image: %s is a directory", file)
	}
	body, err := io.ReadAll(io.LimitReader(f, p.cfg.MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > p.cfg.MaxSourceBytes {
		return nil, fmt.Errorf("opti-image: source is larger than %d bytes", p.cfg.MaxSourceBytes)
	}
	return body, nil
}

// sourceBytes is what a recipe is made from: a local file, which must still be
// the content the name was minted for, or an allowed origin's answer.
func (p *Plugin) sourceBytes(ctx context.Context, r recipe) ([]byte, error) {
	if r.Digest != "" {
		fsys, file, _, ok := p.cfg.local(r.Source)
		if !ok {
			return nil, fmt.Errorf("opti-image: %q is under no Files prefix", r.Source)
		}
		body, err := p.readLocal(fsys, file)
		if err != nil {
			return nil, err
		}
		if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != r.Digest {
			// A recipe from an earlier build, read back from disk: the name
			// stands for content this build no longer has.
			return nil, fmt.Errorf("opti-image: %s has changed since its image was named", r.Source)
		}
		return body, nil
	}
	return p.fetch(ctx, r)
}
