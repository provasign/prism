package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/workspace"
)

// graphRepoStartTimeout bounds how long a linked repository's explorer may
// take to print its address; a first run indexes the whole repository.
const graphRepoStartTimeout = 10 * time.Minute

// repoGraphs starts `prism graph` for linked repositories on request from
// the page and keeps one running explorer per repository until Close.
type repoGraphs struct {
	roots func() []workspace.Root

	mu      sync.Mutex
	running map[string]*repoGraph // "id/tests" -> explorer
}

type repoGraph struct {
	cmd  *exec.Cmd
	url  string
	done chan struct{}
}

func newRepoGraphs(roots func() []workspace.Root) *repoGraphs {
	return &repoGraphs{roots: roots, running: map[string]*repoGraph{}}
}

// Open returns the page address of repository id's explorer, starting it
// when none runs.
func (g *repoGraphs) Open(ctx context.Context, id string, tests bool) (string, error) {
	var root *workspace.Root
	for _, r := range g.roots() {
		if r.ID == id {
			r := r
			root = &r
			break
		}
	}
	if root == nil {
		return "", fmt.Errorf("%s is not a linked repository with a local copy (link server repositories cannot be explored here)", id)
	}
	if why := root.Unusable(); why != "" {
		return "", fmt.Errorf("%s: %s", id, why)
	}
	key := fmt.Sprintf("%s/%v", id, tests)
	g.mu.Lock()
	defer g.mu.Unlock()
	if rg := g.running[key]; rg != nil {
		select {
		case <-rg.done:
			delete(g.running, key)
		default:
			return rg.url, nil
		}
	}
	rg, err := startRepoGraph(ctx, root.Path, tests)
	if err != nil {
		return "", fmt.Errorf("%s: %w", id, err)
	}
	g.running[key] = rg
	return rg.url, nil
}

// Close stops every explorer this one started.
func (g *repoGraphs) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, rg := range g.running {
		stopProcess(rg.cmd, rg.done)
		delete(g.running, key)
	}
}

// startRepoGraph runs `prism graph <dir> --no-open` and waits for the
// address it prints on stdout. Its progress goes to this terminal.
func startRepoGraph(ctx context.Context, dir string, tests bool) (*repoGraph, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"graph", dir, "--no-open"}
	if tests {
		args = append(args, "--tests")
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var tail tailBuffer
	cmd.Stderr = io.MultiWriter(prefixWriter{w: os.Stderr, prefix: "[" + dir + "] "}, &tail)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); strings.HasPrefix(l, "http://") {
				lines <- l
				break
			}
		}
		_, _ = io.Copy(io.Discard, out)
		close(lines)
	}()
	go func() { _ = cmd.Wait(); close(done) }()

	timer := time.NewTimer(graphRepoStartTimeout)
	defer timer.Stop()
	select {
	case u, ok := <-lines:
		if ok {
			return &repoGraph{cmd: cmd, url: u, done: done}, nil
		}
		<-done
		return nil, errors.New("prism graph stopped: " + tail.last())
	case <-timer.C:
	case <-ctx.Done():
	}
	stopProcess(cmd, done)
	return nil, errors.New("prism graph did not start in time: " + tail.last())
}

// stopProcess interrupts the explorer and kills it if it does not exit.
func stopProcess(cmd *exec.Cmd, done <-chan struct{}) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// tailBuffer keeps the last stderr lines for an error message.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return "no output"
}

// prefixWriter tags each line written to w.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	var out bytes.Buffer
	for _, l := range bytes.SplitAfter(b, []byte("\n")) {
		if len(l) > 0 {
			out.WriteString(p.prefix)
			out.Write(l)
		}
	}
	_, err := p.w.Write(out.Bytes())
	return len(b), err
}
