package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWithFileLockSerializesAccess(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "cache.lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- WithFileLock(lockPath, time.Second, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	err := WithFileLock(lockPath, 50*time.Millisecond, func() error {
		return errors.New("should not enter while lock is held")
	})
	if err == nil {
		t.Fatal("expected timeout while lock is held")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first lock holder failed: %v", err)
	}

	if err := WithFileLock(lockPath, time.Second, func() error { return nil }); err != nil {
		t.Fatalf("expected lock after release: %v", err)
	}
}
