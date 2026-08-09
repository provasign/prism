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

	skip := make(map[string]bool, len(excludeDirs))
	for _, d := range excludeDirs {
		skip[d] = true
	}

	// Collect candidate files first (cheap), then scan in parallel.
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (skip[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
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
			hits := scanFile(path, root, match, opts.MaxPerFile)
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
func scanFile(path, root string, match func(string) bool, maxPerFile int) []Hit {
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

	var hits []Hit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	ln := 0
	for sc.Scan() {
		ln++
		line := sc.Text()
		if !match(line) {
			continue
		}
		hits = append(hits, Hit{File: rel, Line: ln, Text: truncateLine(line)})
		if len(hits) >= maxPerFile {
			break
		}
	}
	return hits
}
