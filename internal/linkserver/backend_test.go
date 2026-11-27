package linkserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestFSBlobContract(t *testing.T) {
	b, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobContract(t, b)
}

func TestFSTwoStoresOnOneDirectory(t *testing.T) {
	dir := t.TempDir()
	b1, _ := OpenFS(dir)
	b2, _ := OpenFS(dir)
	s1, err := OpenStore(context.Background(), b1, Options{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(context.Background(), b2, Options{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	replicaContract(t, s1, s2)
}

// blobContract is what every backend must do.
func blobContract(t *testing.T, b Blob) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := b.Get(ctx, "r/missing.json", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if err := b.Put(ctx, "r/a.json", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	data, tag, err := b.Get(ctx, "r/a.json", "")
	if err != nil || string(data) != `{"a":1}` || tag == "" {
		t.Fatalf("get: %q %q %v", data, tag, err)
	}
	if _, _, err := b.Get(ctx, "r/a.json", tag); !errors.Is(err, ErrNotModified) {
		t.Fatalf("conditional get with the current etag: %v", err)
	}
	// Create-only, then compare-and-swap.
	if _, err := b.PutIf(ctx, "r/refs.json", []byte(`1`), ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := b.PutIf(ctx, "r/refs.json", []byte(`2`), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("create over an existing object: %v", err)
	}
	_, t1, _ := b.Get(ctx, "r/refs.json", "")
	t2, err := b.PutIf(ctx, "r/refs.json", []byte(`3`), t1)
	if err != nil {
		t.Fatalf("swap with the current etag: %v", err)
	}
	if _, err := b.PutIf(ctx, "r/refs.json", []byte(`4`), t1); !errors.Is(err, ErrConflict) {
		t.Fatalf("swap with a stale etag: %v", err)
	}
	if got, _, _ := b.Get(ctx, "r/refs.json", ""); string(got) != "3" {
		t.Fatalf("after the lost swap: %q", got)
	}
	_ = t2
	keys, err := b.List(ctx, "r/")
	if err != nil || fmt.Sprint(keys) != "[r/a.json r/refs.json]" {
		t.Fatalf("list: %v %v", keys, err)
	}
	if err := b.Delete(ctx, "r/a.json"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "r/a.json"); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
	if keys, _ := b.List(ctx, "r/"); fmt.Sprint(keys) != "[r/refs.json]" {
		t.Fatalf("list after delete: %v", keys)
	}
	_ = b.Delete(ctx, "r/refs.json")
}

// replicaContract: two stores on one backend.
func replicaContract(t *testing.T, s1, s2 *Store) {
	t.Helper()
	ctx := context.Background()
	if err := s1.Put(Bundle{Format: 1, Repo: "example.org/app", SHA: "c1", Ref: "main",
		Refs: []Site{{Key: "go:example.com/lib#F", File: "a.go", Line: 1}}}); err != nil {
		t.Fatal(err)
	}
	if got := s2.Consumers(ConsumersRequest{Keys: []string{"go:example.com/lib#F"}}); len(got.Repos) != 0 {
		t.Fatalf("replica 2 answered before a refresh: %+v", got)
	}
	if err := s2.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s2.Consumers(ConsumersRequest{Keys: []string{"go:example.com/lib#F"}}); len(got.Repos) != 1 || got.Repos[0].SHA != "c1" {
		t.Fatalf("replica 2 after refresh: %+v", got)
	}
	// Concurrent uploads of different branches through both replicas:
	// compare-and-swap keeps every branch.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		for j, s := range []*Store{s1, s2} {
			wg.Add(1)
			go func(i, j int, s *Store) {
				defer wg.Done()
				errs <- s.Put(Bundle{Format: 1, Repo: "example.org/app", SHA: fmt.Sprintf("b%d-%d", i, j), Ref: fmt.Sprintf("br%d-%d", i, j)})
			}(i, j, s)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s1.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	refs := s1.refsOf("example.org/app")
	if len(refs.Refs) != 21 { // main + 20 branches
		t.Fatalf("branches after concurrent uploads: %d, want 21: %v", len(refs.Refs), refs.Refs)
	}
	if _, err := s1.DeleteRepo(ctx, "example.org/app"); err != nil {
		t.Fatal(err)
	}
}
