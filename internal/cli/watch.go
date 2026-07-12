package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchIgnoreDirs are never watched or reindexed — build output, VCS, vendored
// trees, and other engines' index dirs. Matched by base name at any depth.
var watchIgnoreDirs = map[string]bool{
	".git": true, ".grove": true, ".codegraph": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".next": true, ".venv": true, "__pycache__": true,
}

// cmdWatch keeps the Grove index warm by PUSH: it watches the working tree and
// delta-reindexes on save (debounced), so `query`/`change_impact`/`affected`
// never wait on a stale-index rebuild. Complements the pull model (each command
// delta-indexes on demand) for long editor/agent sessions.
func cmdWatch(args []string) int {
	dir := "."
	debounce := 2 * time.Second
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--debounce":
			if i+1 < len(args) {
				if d, err := time.ParseDuration(args[i+1]); err == nil && d > 0 {
					debounce = d
				}
				i++
			}
		default:
			if !strings.HasPrefix(a, "-") {
				dir = a
			}
		}
	}

	_, client, err := newClient(dir)
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

	addTree := func(base string) {
		filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != base && watchIgnoreDirs[d.Name()] {
					return filepath.SkipDir
				}
				_ = w.Add(p)
			}
			return nil
		})
	}
	addTree(root)

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
					if !watchIgnoreDirs[filepath.Base(ev.Name)] {
						addTree(ev.Name)
					}
					continue
				}
			}
			if watchIgnorePath(root, ev.Name) {
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

// watchIgnorePath filters events under ignored directories (a change deep in
// node_modules/vendor should never trigger a reindex).
func watchIgnorePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if watchIgnoreDirs[part] {
			return true
		}
	}
	return false
}
