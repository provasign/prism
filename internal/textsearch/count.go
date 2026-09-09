package textsearch

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FileCount is the exact number of matching lines in one file.
type FileCount struct {
	File  string `json:"file"`
	Count int    `json:"count"`
}

// CountResult describes a compact count-only pass. Complete is false when the
// deadline fires; callers must then treat TotalHits as a lower bound.
type CountResult struct {
	Backend       string      `json:"backend"`
	TotalHits     int         `json:"totalHits"`
	FilesMatched  int         `json:"filesMatched"`
	Files         []FileCount `json:"files,omitempty"`
	Complete      bool        `json:"complete"`
	TimedOut      bool        `json:"timedOut,omitempty"`
	RejectedPaths []string    `json:"rejectedPaths,omitempty"`
}

// Count counts matching lines without retaining their source text. It uses the
// same backend, ignore rules, path scopes, globs, and regex semantics as Search.
func Count(ctx context.Context, root, pattern string, opts Options) CountResult {
	opts = opts.withDefaults()
	if strings.TrimSpace(pattern) == "" {
		return CountResult{Backend: Backend(), Complete: true}
	}
	switch Backend() {
	case "rg":
		if r, ok := runRgCount(ctx, root, pattern, opts); ok {
			return r
		}
	case "grep":
		if r, ok := runGrepCount(ctx, root, pattern, opts); ok {
			return r
		}
	}
	return nativeCount(ctx, root, pattern, opts)
}

func runRgCount(ctx context.Context, root, pattern string, opts Options) (CountResult, bool) {
	args := []string{
		"--count", "--with-filename", "--color", "never", "--no-messages",
		"--no-require-git", "--hidden", "--sort", "path", "--max-filesize", "2M",
	}
	if !regexUsable(pattern, opts) {
		args = append(args, "--fixed-strings")
	}
	for _, d := range excludeDirs {
		args = append(args, "--glob", "!"+d+"/")
	}
	for _, g := range opts.Glob {
		args = append(args, "--glob", g)
	}
	operands, rejected := scopeArgs(root, opts.Paths)
	if len(operands) == 0 {
		return CountResult{Backend: "rg", Complete: true, RejectedPaths: rejected}, true
	}
	args = append(args, "-e", pattern, "--")
	args = append(args, operands...)
	r, ok := runCountTool(ctx, root, bin("rg"), args, nil)
	r.RejectedPaths = rejected
	return r, ok
}

func runGrepCount(ctx context.Context, root, pattern string, opts Options) (CountResult, bool) {
	mode := "-F"
	if regexUsable(pattern, opts) {
		mode = "-E"
	}
	args := []string{"-rHIc", mode, "-i"}
	for _, d := range append(append([]string{}, excludeDirs...), gitignoreDirs(root)...) {
		args = append(args, "--exclude-dir="+d)
	}
	for _, g := range opts.Glob {
		args = append(args, "--include="+g)
	}
	operands, rejected := scopeArgs(root, opts.Paths)
	if len(operands) == 0 {
		return CountResult{Backend: "grep", Complete: true, RejectedPaths: rejected}, true
	}
	args = append(args, "-e", pattern, "--")
	args = append(args, operands...)
	r, ok := runCountTool(ctx, root, bin("grep"), args, []string{"LC_ALL=C"})
	r.RejectedPaths = rejected
	return r, ok
}

func runCountTool(ctx context.Context, root, executable string, args, extraEnv []string) (CountResult, bool) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = root
	if extraEnv != nil {
		cmd.Env = append(cmd.Environ(), extraEnv...)
	}
	out, err := cmd.Output()
	r := CountResult{Backend: filepath.Base(executable)}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		sep := strings.LastIndexByte(line, ':')
		if sep < 0 {
			return r, false
		}
		n, parseErr := strconv.Atoi(strings.TrimSpace(line[sep+1:]))
		if parseErr != nil || n < 0 {
			return r, false
		}
		if n == 0 {
			continue
		}
		file := filepath.ToSlash(filepath.Clean(line[:sep]))
		r.Files = append(r.Files, FileCount{File: file, Count: n})
		r.TotalHits += n
	}
	sort.Slice(r.Files, func(i, j int) bool { return r.Files[i].File < r.Files[j].File })
	r.FilesMatched = len(r.Files)
	if ctx.Err() != nil {
		r.TimedOut = true
		return r, true
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return r, false
		}
	}
	r.Complete = true
	return r, true
}

func nativeCount(ctx context.Context, root, pattern string, opts Options) CountResult {
	r := CountResult{Backend: "native"}
	match := countMatcher(pattern, opts)
	resolvedRoot := root
	if rr, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = rr
	}
	skip := map[string]bool{}
	for _, d := range append(append([]string{}, excludeDirs...), gitignoreDirs(root)...) {
		skip[d] = true
	}
	roots, rejected := scopeArgs(root, opts.Paths)
	r.RejectedPaths = rejected
	var files []string
	for _, scoped := range roots {
		scanRoot := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(scoped, "./")))
		_ = filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if path != root && skip[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				resolved, resolveErr := filepath.EvalSymlinks(path)
				if resolveErr != nil {
					return nil
				}
				rel, relErr := filepath.Rel(resolvedRoot, resolved)
				if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return nil
				}
			}
			if info, infoErr := d.Info(); infoErr != nil || info.Size() > 2<<20 {
				return nil
			}
			if len(opts.Glob) > 0 {
				matched := false
				for _, glob := range opts.Glob {
					if ok, _ := filepath.Match(glob, d.Name()); ok {
						matched = true
						break
					}
				}
				if !matched {
					return nil
				}
			}
			files = append(files, path)
			return nil
		})
	}
	sort.Strings(files)
	for _, path := range files {
		if ctx.Err() != nil {
			r.TimedOut = true
			return r
		}
		n := countFile(path, match)
		if n == 0 {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		r.Files = append(r.Files, FileCount{File: filepath.ToSlash(rel), Count: n})
		r.TotalHits += n
	}
	r.FilesMatched = len(r.Files)
	r.Complete = true
	return r
}

func countMatcher(pattern string, opts Options) func(string) bool {
	if regexUsable(pattern, opts) {
		re := regexp.MustCompile("(?i)" + pattern)
		return re.MatchString
	}
	needle := strings.ToLower(pattern)
	return func(line string) bool { return strings.Contains(strings.ToLower(line), needle) }
}

func countFile(path string, match func(string) bool) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return 0
	}
	if _, err := f.Seek(0, 0); err != nil {
		return 0
	}
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		if match(scanner.Text()) {
			count++
		}
	}
	return count
}
