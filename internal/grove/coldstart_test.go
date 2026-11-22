package grove

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// BUG-11: parallel first commands on an unindexed repository all succeed
// and only one of them builds the index.
func TestParallelColdStartIndexesOnce(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"a.go", "b.go", "c.go"} {
		writePrismFile(t, dir, name, "package p\n\nfunc F"+string(rune('A'+i))+"() {}\n")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := NewClient("", "").WithTokenFromDir(dir).OneShot()
			if err := c.EnsureRunning(context.Background()); err != nil {
				errs[i] = err
				return
			}
			defer c.Shutdown()
			errs[i] = c.AutoIndexIfEmpty(context.Background())
		}(i)
	}
	wg.Wait()
	os.Stderr = saved
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	for i, err := range errs {
		if err != nil {
			t.Errorf("client %d: %v", i, err)
		}
	}
	if n := strings.Count(string(stderr), "building the index"); n != 1 {
		t.Fatalf("%d clients built the index, want 1:\n%s", n, stderr)
	}
}

// BUG-21: an ordinary test caller is "test", not "test double".
func TestTestLabel(t *testing.T) {
	cases := map[[2]string]string{
		{"pkg/a_test.go", "TestThing"}:    "test",
		{"pkg/mock_store.go", "Store"}:    "test double",
		{"pkg/a_test.go", "fakeClient"}:   "test double",
		{"pkg/testdata/x/y.go", "Helper"}: "test",
		{"pkg/real.go", "Thing"}:          "",
	}
	for in, want := range cases {
		if got := testLabel(in[0], in[1]); got != want {
			t.Errorf("testLabel(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
