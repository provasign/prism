package textsearch

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// nativeSearch is the zero-dependency fallback: a parallel walk + per-line
// case-insensitive substring scan. It is deliberately honest about what it
// is NOT — no literal prefiltering, no .gitignore parsing beyond the fixed
// exclude list — so it is slower than rg on big repos, but it behaves
// identically on every OS and needs nothing installed. prism doctor reports
// which backend is active so the difference is visible, never silent.
func nativeSearch(ctx context.Context, root, pattern string, opts Options) Result {
	res := Result{Backend: "native"}
	needle := strings.ToLower(pattern)
	// Regex mode: precompile case-insensitively; a non-compiling pattern
	// already degraded to literal in regexUsable.
	var re *regexp.Regexp
	if regexUsable(pattern, opts) {
		re = regexp.MustCompile("(?i)" + pattern)
	}
	match := func(line string) bool {
		if re != nil {
			return re.MatchString(line)
		}
		return strings.Contains(strings.ToLower(line), needle)
	}

	// Same rule as the shelled-out engines: skip what the PROJECT says to
	// skip, not what prism assumes. Hidden paths are searched (grep -r
	// semantics, matching runRg's --hidden); only prism state, the VCS dir,
	// and root-.gitignore directory names are excluded.
	skip := map[string]bool{}
	for _, d := range append(append([]string{}, excludeDirs...), gitignoreDirs(root)...) {
		skip[d] = true
	}

	// Collect candidate files first (cheap), then scan in parallel.
	// Resolve root once so the symlink containment check below compares
	// against the real tree (macOS: /tmp is itself a symlink to /private/tmp
	// and would otherwise reject every in-root target).
	resolvedRoot := root
	if rr, rerr := filepath.EvalSymlinks(root); rerr == nil {
		resolvedRoot = rr
	}
	walkRoot := root
	if opts.Path != "" {
		// grep's target-path semantics: search one file or subtree only.
		walkRoot = filepath.Join(root, filepath.FromSlash(opts.Path))
	}
	var files []string
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && skip[name] {
				return filepath.SkipDir
			}
			return nil
		}
		// A symlinked FILE entry can point anywhere; WalkDir already refuses
		// to descend into symlinked directories, but without this check a
		// repository symlink would expose external file contents through
		// search results. Reject any entry whose resolved target escapes root
		// (same containment rule as Grove's indexer).
		if d.Type()&fs.ModeSymlink != 0 {
			resolved, rerr := filepath.EvalSymlinks(path)
			if rerr != nil {
				return nil
			}
			if r, rerr := filepath.Rel(resolvedRoot, resolved); rerr != nil || r == ".." ||
				strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return nil
			}
		}
		if info, ierr := d.Info(); ierr != nil || info.Size() > 2<<20 {
			return nil // unreadable or >2MB: same cap as the rg invocation
		}
		files = append(files, path)
		return nil
	})

	var (
		mu      sync.Mutex
		results []fileHits
		total   int
	)
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, path := range files {
		mu.Lock()
		done := total >= opts.MaxHits
		mu.Unlock()
		if done || ctx.Err() != nil {
			res.Truncated = done
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			hits := scanFile(path, root, match, opts.MaxPerFile, opts.Context, opts.FilesOnly)
			if len(hits) == 0 {
				return
			}
			mu.Lock()
			results = append(results, fileHits{idx, hits})
			total += len(hits)
			mu.Unlock()
		}(i, path)
	}
	wg.Wait()

	// Deterministic output: walk order, not goroutine completion order.
	if ctx.Err() != nil {
		res.TimedOut = true
	}
	sortFileHits(results)
	for _, fh := range results {
		for _, h := range fh.hits {
			if len(res.Hits) >= opts.MaxHits {
				res.Truncated = true
				return res
			}
			res.Hits = append(res.Hits, h)
		}
	}
	return res
}

type fileHits struct {
	idx  int
	hits []Hit
}

func sortFileHits(fhs []fileHits) {
	sort.Slice(fhs, func(i, j int) bool { return fhs[i].idx < fhs[j].idx })
}

// scanFile returns up to maxPerFile case-insensitive substring hits in one
// file, skipping binaries (NUL byte in the first 1KB).
func scanFile(path, root string, match func(string) bool, maxPerFile, context int, filesOnly bool) []Hit {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	head := make([]byte, 1024)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil // binary
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	var hits []Hit
	emitted := map[int]bool{}
	matches := 0
	for i, line := range lines {
		if !match(line) {
			continue
		}
		if filesOnly {
			return []Hit{{File: rel}}
		}
		matches++
		lo, hi := i-context, i+context
		if lo < 0 {
			lo = 0
		}
		if hi > len(lines)-1 {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			if !emitted[j] {
				emitted[j] = true
				hits = append(hits, Hit{File: rel, Line: j + 1, Text: truncateLine(lines[j])})
			}
		}
		if matches >= maxPerFile {
			break
		}
	}
	return hits
}
