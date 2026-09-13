package textsearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunLineToolMarksPartialOutputTimedOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	marker := filepath.Join(root, "output-written")
	type result struct {
		r  Result
		ok bool
	}
	results := make(chan result, 1)
	go func() {
		r, ok := runLineTool(ctx, root, os.Args[0],
			[]string{"-test.run=^TestRunLineToolPartialOutputHelper$"},
			[]string{"PRISM_LINE_TOOL_TEST_HELPER=1", "PRISM_LINE_TOOL_TEST_MARKER=" + marker},
			Options{MaxHits: 10, MaxPerFile: 10})
		results <- result{r, ok}
	}()

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("helper did not write partial output")
		}
	}
	cancel()
	var got result
	select {
	case got = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("line tool did not stop after cancellation")
	}
	r, ok := got.r, got.ok
	if !ok || len(r.Hits) != 1 || !r.TimedOut {
		t.Fatalf("partial output after deadline must be marked timed out: ok=%t result=%+v", ok, r)
	}
}

func TestRunLineToolPartialOutputHelper(t *testing.T) {
	if os.Getenv("PRISM_LINE_TOOL_TEST_HELPER") != "1" {
		return
	}
	fmt.Println("a.go:1:Foo")
	if err := os.WriteFile(os.Getenv("PRISM_LINE_TOOL_TEST_MARKER"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second)
}
