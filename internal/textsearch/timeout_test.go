package textsearch

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestRunLineToolMarksPartialOutputTimedOut(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r, ok := runLineTool(ctx, t.TempDir(), sh,
		[]string{"-c", "printf 'a.go:1:Foo\\n'; sleep 1"}, nil,
		Options{MaxHits: 10, MaxPerFile: 10})
	if !ok || len(r.Hits) != 1 || !r.TimedOut {
		t.Fatalf("partial output after deadline must be marked timed out: ok=%t result=%+v", ok, r)
	}
}
