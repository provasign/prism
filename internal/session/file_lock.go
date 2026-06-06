package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WithFileLock runs fn while holding an exclusive lock file. It is intended
// for short critical sections around cross-process cache/ledger updates.
func WithFileLock(path string, timeout time.Duration, fn func() error) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	staleAfter := 30 * time.Second
	if timeout*10 > staleAfter {
		staleAfter = timeout * 10
	}
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			_ = f.Close()
			defer os.Remove(path)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleAfter {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for lock %s", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
