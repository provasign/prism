// Package textsearch gives Prism a real full-text search: the literal/substring
// search an agent would otherwise reach for grep to do. It shells out to the
// best engine present on the host — ripgrep, then grep — and falls back to a
// built-in Go scanner so the capability exists on every platform with zero
// external dependency. The backend choice is Prism's, made once per process;
// callers (and agents) never pick a binary.
//
// Safety: the pattern is always passed after `-e` and paths after `--`, so a
// pattern beginning with `-` can never be parsed as an option (same
// flag-injection posture as verify.go's git guards). Patterns are matched as
// FIXED STRINGS, case-insensitive — the semantics of an agent's search term,
// not a regex engine.
package textsearch

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hit is one matching line. File is root-relative with forward slashes.
type Hit struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Result is the outcome of one search.
type Result struct {
	Hits    []Hit  `json:"hits"`
	Backend string `json:"backend"` // "rg" | "grep" | "native"
	// Truncated: hit caps were reached. TimedOut: the deadline fired before
	// the search finished — the distinction matters because an empty result
	// from a timeout is indistinguishable from "no matches" to a caller, and
	// silently reads as "that string is not in the repo" (measured: a native
	// scan of an unexcluded virtualenv returned nothing for a string in the
	// project's own source).
	Truncated bool `json:"truncated,omitempty"`
	TimedOut  bool `json:"timedOut,omitempty"`
}

// Options bound a search. Zero values get safe defaults.
type Options struct {
	MaxHits    int           // total hits cap (default 100)
	MaxPerFile int           // per-file hits cap (default 20)
	Timeout    time.Duration // default 10s
	// Regex treats pattern as a regular expression instead of a fixed
	// string. A pattern that does not compile falls back to literal — a
	// search that errors is a tool agents route around.
	Regex bool
}

func (o Options) withDefaults() Options {
	if o.MaxHits <= 0 {
		o.MaxHits = 100
	}
	if o.MaxPerFile <= 0 {
		o.MaxPerFile = 20
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	return o
}

// When an agent asks for a text search it is asking for ripgrep's semantics,
// not prism's opinion of what it meant. Prism does NOT add its own exclusion
// list: rg already skips .gitignore'd trees, hidden directories and binaries,
// and a virtualenv is normally gitignored — so the case that motivated an
// exclusion list is handled by the engine the agent expected. Overriding that
// made prism silently unable to answer "where does this library define X",
// which is a legitimate question about code that is really there.
//
// excludeDirs is therefore only what rg itself would skip and the fallbacks
// cannot infer: prism's own state and the VCS directory. Everything else is
// the agent's call.
var excludeDirs = []string{".git", ".grove"}

// gitignoreDirs approximates rg's .gitignore handling for the grep and native
// fallbacks, which have none. Read from the repo's own .gitignore so prism
// skips what the project says to skip — no prism-authored policy.
//
// Known divergence from rg, stated rather than implied: only the ROOT
// .gitignore is read, only plain directory names are honored (no globs, no
// negations, no nested ignore files). On a repo that leans on those, the
// grep/native fallbacks search MORE than rg would — never less — so the
// failure mode is extra hits, not silent absence. `prism doctor` reports
// which backend is active.
func gitignoreDirs(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		l = strings.TrimSuffix(strings.TrimPrefix(l, "/"), "/")
		// Only plain directory names: a real glob needs a matcher, and
		// guessing at one would skip files the agent asked about.
		if l != "" && !strings.ContainsAny(l, "*?[]") {
			out = append(out, l)
		}
	}
	return out
}

// maxLineLen truncates pathological lines (minified JS) so one hit cannot
// blow the token budget it exists to protect.
const maxLineLen = 250

var (
	detectOnce  sync.Once
	backend     string
	backendPath string // absolute path when PATH lookup failed but the binary exists
)

// lookup finds a binary on PATH, then at known absolute locations.
func lookup(name string, fallbacks ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range fallbacks {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// bin returns the executable to run for name: the resolved absolute path when
// name IS the detected backend, else name itself. Returning backendPath
// unconditionally made runGrep execute ripgrep whenever rg was the detected
// backend — the two take different flags, so the call simply failed.
func bin(name string) string {
	if name == Backend() && backendPath != "" {
		return backendPath
	}
	return name
}

// Backend reports which engine this process will use: "rg", "grep", or
// "native". Detected once; the answer never changes within a process.
func Backend() string {
	detectOnce.Do(func() {
		// An MCP server is spawned by the editor, not by a login shell, so its
		// PATH can be minimal or empty — measured: a server reporting backend
		// "native" on a machine whose shell found grep fine. Fall back to the
		// usual absolute locations before giving up on the fast engines.
		if p := lookup("rg", "/opt/homebrew/bin/rg", "/usr/local/bin/rg", "/usr/bin/rg"); p != "" {
			backend, backendPath = "rg", p
			return
		}
		if p := lookup("grep", "/usr/bin/grep", "/bin/grep", "/opt/homebrew/bin/grep"); p != "" {
			backend, backendPath = "grep", p
			return
		}
		backend = "native"
	})
	return backend
}

// Search finds lines containing pattern (fixed string, case-insensitive)
// under root. It never returns an error for "no matches" — that is an empty
// Result. Engine failures fall back to the native scanner rather than
// erroring: a text search that sometimes fails is a tool agents route around.
func Search(ctx context.Context, root, pattern string, opts Options) Result {
	opts = opts.withDefaults()
	if strings.TrimSpace(pattern) == "" {
		return Result{Backend: Backend()}
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	switch Backend() {
	case "rg":
		if r, ok := runRg(ctx, root, pattern, opts); ok {
			return r
		}
	case "grep":
		if r, ok := runGrep(ctx, root, pattern, opts); ok {
			return r
		}
	}
	return nativeSearch(ctx, root, pattern, opts)
}

// runRg shells out to ripgrep. ok=false means the invocation itself failed
// (not "no matches") and the caller should fall back.
func runRg(ctx context.Context, root, pattern string, opts Options) (Result, bool) {
	args := []string{
		"--no-config", "--line-number", "--no-heading", "--color=never",
		"--ignore-case",
		// rg honors .gitignore only INSIDE a git repository. A worktree, an
		// unpacked tarball or a subdirectory checkout has the ignore file but
		// no .git, and rg would then search everything the project asked it
		// not to — including the venv that started all of this.
		"--no-require-git",
		"--max-count", strconv.Itoa(opts.MaxPerFile),
		"--max-columns", strconv.Itoa(maxLineLen), "--max-columns-preview",
		"--max-filesize", "2M",
	}
	if !regexUsable(pattern, opts) {
		args = append(args, "--fixed-strings")
	}
	// No prism-authored globs: rg's defaults ARE the requested semantics.
	args = append(args, "--glob", "!.grove/", "-e", pattern, "--", ".")
	return runLineTool(ctx, root, bin("rg"), args, nil, opts)
}

// hiddenDirNames walks root (directories only, bounded) and returns the
// distinct basenames of hidden directories, for literal --exclude-dir flags.
// Literal names because "grep" may be GNU, BSD or ugrep and their glob
// semantics for --exclude-dir disagree.
func hiddenDirNames(root string, cap int) []string {
	seen := map[string]bool{}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if p != root && strings.HasPrefix(name, ".") {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			return filepath.SkipDir // no need to walk inside what we exclude
		}
		if len(out) >= cap {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// runGrep shells out to grep, pinned to the C locale — under a UTF-8 locale
// GNU grep's multibyte path is several times slower, and byte-oriented
// matching is exactly right for fixed-string code search.
func runGrep(ctx context.Context, root, pattern string, opts Options) (Result, bool) {
	mode := "-F"
	if regexUsable(pattern, opts) {
		mode = "-E"
	}
	// Backend parity: rg skips hidden directories by default and the native
	// scanner mirrors that; grep alone would crawl .venv/.tox/.idea, giving
	// the machine that lacks rg a DIFFERENT corpus for the same query.
	// LITERAL names, not a glob: "grep" may be GNU, BSD or ugrep, and their
	// --exclude-dir glob semantics disagree (measured: ugrep's ".?*"
	// excluded a plain "sub/"). Enumerating the hidden dirs that actually
	// exist sidesteps every implementation.
	args := []string{"-rnI", mode, "-i", "--max-count", strconv.Itoa(opts.MaxPerFile)}
	for _, d := range hiddenDirNames(root, 64) {
		args = append(args, "--exclude-dir="+d)
	}
	for _, d := range append(append([]string{}, excludeDirs...), gitignoreDirs(root)...) {
		args = append(args, "--exclude-dir="+d)
	}
	args = append(args, "-e", pattern, "--", ".")
	return runLineTool(ctx, root, bin("grep"), args, []string{"LC_ALL=C"}, opts)
}

// runLineTool runs an external searcher emitting `path:line:text` lines and
// parses them into Hits. Exit code 1 with empty output is "no matches", a
// success. Anything else that produced no parseable output is a failure and
// the caller falls back to the native scanner.
func runLineTool(ctx context.Context, root, bin string, args, extraEnv []string, opts Options) (Result, bool) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = root
	if extraEnv != nil {
		cmd.Env = append(cmd.Environ(), extraEnv...)
	}
	out, err := cmd.Output()
	res := Result{Backend: filepath.Base(bin)}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if len(res.Hits) >= opts.MaxHits {
			res.Truncated = true
			break
		}
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		ln, aerr := strconv.Atoi(parts[1])
		if aerr != nil || ln < 1 {
			continue
		}
		res.Hits = append(res.Hits, Hit{
			File: filepath.ToSlash(strings.TrimPrefix(parts[0], "./")),
			Line: ln,
			Text: truncateLine(parts[2]),
		})
	}
	if err != nil && len(res.Hits) == 0 {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return res, true // exit 1 = no matches: a real, empty answer
		}
		return res, false // engine failure: let the caller fall back
	}
	return res, true
}

// regexUsable reports whether the pattern should run as a regex: requested,
// and it compiles under Go syntax (the common denominator across rg/grep -E/
// the native scanner). A non-compiling pattern degrades to literal everywhere
// so all three backends stay behavior-identical.
func regexUsable(pattern string, opts Options) bool {
	if !opts.Regex {
		return false
	}
	_, err := regexp.Compile("(?i)" + pattern)
	return err == nil
}

func truncateLine(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > maxLineLen {
		return s[:maxLineLen] + "…"
	}
	return s
}
