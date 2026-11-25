package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/provasign/grove/pkg/grove"
)

// watchTree decides what prism watch watches: the directories and files the
// index reads (grove.WalkFilter: .gitignore, .groveignore, the engine's skip
// list) minus hidden directories. Those hold tool state (VCS metadata, index
// caches, editor config), and a watcher that follows them spins on another
// tool's own writes; source kept in one is still indexed by `prism index`.
type watchTree struct {
	root   string
	filter *grove.WalkFilter
	add    func(string) error // fsnotify.Watcher.Add

	watched, failed int
	lastErr         error
	warned          bool
}

func newWatchTree(root string, add func(string) error) *watchTree {
	return &watchTree{root: root, filter: grove.NewWalkFilter(root), add: add}
}

// addTree watches base and every directory below it that the index reads,
// and reports whether base itself is one. Parents must be added before their
// children. A failed watch is counted, not dropped: on a large tree the usual
// cause is the open-file limit, and a silent gap means saves there never
// reindex.
func (t *watchTree) addTree(base string) bool {
	entered := false
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != t.root && (strings.HasPrefix(d.Name(), ".") || !t.filter.Enter(p)) {
			return filepath.SkipDir
		}
		if p == base {
			entered = true
		}
		if err := t.add(p); err != nil {
			t.failed++
			t.lastErr = err
		} else {
			t.watched++
		}
		return nil
	})
	return entered
}

// warnFailures prints the failed-watch count once per session.
func (t *watchTree) warnFailures(w io.Writer) {
	if t.failed == 0 || t.warned {
		return
	}
	t.warned = true
	fmt.Fprintf(w, "prism watch: watching %d of %d directories (%v); saves in the rest do not reindex. Raise the open-file limit (ulimit -n) and restart\n",
		t.watched, t.watched+t.failed, t.lastErr)
}

// relevant reports whether a change at path can affect the index: it is
// under the root, outside hidden directories and not ignored.
func (t *watchTree) relevant(path string) bool {
	rel, err := filepath.Rel(t.root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return !t.filter.FileIgnored(path)
}

// cmdWatch keeps the Grove index warm by PUSH: it watches the working tree and
// delta-reindexes on save (debounced), so `query`/`change_impact`/`affected`
// never wait on a stale-index rebuild. Complements the pull model (each command
// delta-indexes on demand) for long editor/agent sessions.
func cmdWatch(args []string) int {
	indexAnywhere = true
	defer func() { indexAnywhere = false }()
	dir, dirSet := ".", false
	debounce := 2 * time.Second
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--debounce":
			v, err := flagValue("watch", args, i)
			if err != nil {
				return usageError(err)
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return usageError(fmt.Errorf("watch: --debounce wants a positive duration such as 2s, got %q", v))
			}
			debounce = d
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("watch", a)
			}
			if err := positionalDir("watch", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}

	_, client, err := newClientMode(dir, false) // long-lived: keep resident workers
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Shutdown()
	root := mustAbs(dir)

	ctx := context.Background()
	if res, err := client.Index(ctx, root); err != nil {
		fmt.Fprintln(os.Stderr, "watch: initial index:", err)
		return 1
	} else {
		fmt.Fprintf(os.Stderr, "prism watch: %s — indexed %d symbols; watching (debounce %s, Ctrl+C to stop)\n",
			root, res.SymbolCount, debounce)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "watch:", err)
		return 1
	}
	defer w.Close()

	tree := newWatchTree(root, w.Add)
	_ = tree.addTree(root)
	tree.warnFailures(os.Stderr)

	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	scheduleReindex := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounce, func() {
			res, err := client.Index(ctx, root)
			if err != nil {
				fmt.Fprintln(os.Stderr, "prism watch: reindex failed:", err)
				return
			}
			if res.FilesUpdated > 0 || res.FilesPruned > 0 {
				fmt.Fprintf(os.Stderr, "prism watch: reindexed (%d updated, %d pruned; %d symbols)\n",
					res.FilesUpdated, res.FilesPruned, res.SymbolCount)
			}
		})
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case ev := <-w.Events:
			// A newly created directory must be watched too (git checkout,
			// new package) — fsnotify does not recurse on its own.
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if tree.relevant(ev.Name) && tree.addTree(ev.Name) {
						tree.warnFailures(os.Stderr)
						// The files inside arrived before their watch existed,
						// so they will never produce events of their own.
						scheduleReindex()
					}
					continue
				}
			}
			if !tree.relevant(ev.Name) {
				continue
			}
			scheduleReindex()
		case err := <-w.Errors:
			if err != nil {
				fmt.Fprintln(os.Stderr, "prism watch:", err)
			}
		case <-sigc:
			fmt.Fprintln(os.Stderr, "\nprism watch: stopped")
			return 0
		}
	}
}
