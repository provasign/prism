package linkserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Blob is the storage a Store runs on: named objects with an ETag, and a
// compare-and-swap write so several server replicas can move a
// repository's branches without losing each other's updates. Keys are
// slash-separated ("github.com~acme~app/refs.json").
type Blob interface {
	// Get returns the object and its ETag. With ifNoneMatch equal to the
	// current ETag it returns ErrNotModified; a missing object is ErrNotFound.
	Get(ctx context.Context, key, ifNoneMatch string) ([]byte, string, error)
	// Put writes an object unconditionally.
	Put(ctx context.Context, key string, data []byte) error
	// PutIf writes only if the object's ETag is still ifMatch; an empty
	// ifMatch means the object must not exist yet. A lost race is ErrConflict.
	PutIf(ctx context.Context, key string, data []byte, ifMatch string) (string, error)
	// List returns every key under prefix, sorted.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes an object; a missing one is not an error.
	Delete(ctx context.Context, key string) error
	// Describe names the backend for logs ("dir /var/lib/prism-links").
	Describe() string
}

var (
	ErrNotFound    = errors.New("not found")
	ErrNotModified = errors.New("not modified")
	ErrConflict    = errors.New("changed by another writer")
)

// FSBlob stores objects as files under a directory. It serves one host:
// compare-and-swap holds a lock file per key, which is safe between the
// processes of one machine but not across machines sharing a network file
// system. Several replicas need the S3 backend.
type FSBlob struct {
	dir string
	mu  sync.Mutex // serialises PutIf inside this process
}

// OpenFS returns a filesystem backend rooted at dir, creating it.
func OpenFS(dir string) (*FSBlob, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FSBlob{dir: dir}, nil
}

func (b *FSBlob) Describe() string { return "dir " + b.dir }

func (b *FSBlob) path(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if key == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("bad object key " + key)
	}
	return filepath.Join(b.dir, clean), nil
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func (b *FSBlob) Get(_ context.Context, key, ifNoneMatch string) ([]byte, string, error) {
	p, err := b.path(key)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	tag := etagOf(data)
	if ifNoneMatch != "" && ifNoneMatch == tag {
		return nil, tag, ErrNotModified
	}
	return data, tag, nil
}

func (b *FSBlob) Put(_ context.Context, key string, data []byte) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, data)
}

func (b *FSBlob) PutIf(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	p, err := b.path(key)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	unlock, err := lockFile(ctx, p+".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	cur, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if ifMatch != "" {
			return "", ErrConflict
		}
	case err != nil:
		return "", err
	default:
		if ifMatch == "" || etagOf(cur) != ifMatch {
			return "", ErrConflict
		}
	}
	if err := writeFileAtomic(p, data); err != nil {
		return "", err
	}
	return etagOf(data), nil
}

func (b *FSBlob) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(b.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".lock") {
			return nil
		}
		rel, err := filepath.Rel(b.dir, p)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func (b *FSBlob) Delete(_ context.Context, key string) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Drop the repository directory once it is empty.
	_ = os.Remove(filepath.Dir(p))
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// lockFile takes an exclusive lock by creating path; a lock older than 30 s
// is taken to belong to a crashed process and is broken.
func lockFile(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > 30*time.Second {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrConflict
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
