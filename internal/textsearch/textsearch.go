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
	// TotalHits is how many matches the backend actually emitted and
	// FilesMatched how many files they span -- reported even when Hits is
	// capped, so truncation always carries a denominator.
	//
	// LOWER BOUND, not the exact total: the backend is invoked with a
	// per-file cap (MaxPerFile), so a file with 200 matches contributes at
	// most that many. Callers must present it as "at least N".
	TotalHits    int `json:"totalHits,omitempty"`
	FilesMatched int `json:"filesMatched,omitempty"`
	// RejectedPaths lists requested scopes that resolved outside the root
	// and were dropped. Never silent: a search that quietly widened from
	// one directory to the whole tree returns plausible hits from the wrong
	// place, which is worse than an error.
	RejectedPaths []string `json:"rejectedPaths,omitempty"`
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
	// Paths restricts the search to these repo-relative files or
	// directories. Empty means the whole tree.
	//
	// This exists because agents overwhelmingly search SCOPED. 511 of 946
	// mined real grep invocations named a path (v0.51.0 mining), and 12 of
	// 20 in the 2026-08-15 A/B cells did. Without it prism_search cannot
	// express `grep -n alias octodns/manager.py`, so an agent that knows
	// which file it wants reaches for grep instead — measured as the main
	// reason prism went unused in cells where the issue named its location.
	//
	// Entries that escape the root are DROPPED, and the caller is told
	// which: silently searching the whole tree after being asked for one
	// directory would return plausible hits from the wrong place.
	Paths []string
	// Glob restricts the search to files matching these shell globs
	// (rg --glob / grep --include), e.g. "*.py".
	Glob []string
	// Exhaustive removes both caps: every match, from every file. The agent
	// declares this -- prism does not guess. A "where is X" question wants a
	// sample and a count; "rewrite every .format() call" wants all 1,024
	// sites, and a capped answer to the second is worse than useless because
	// it looks complete. Pair with FilesOnly or Paths unless thousands of
	// lines are genuinely wanted.
	Exhaustive bool
	// FilesOnly is honoured by the CALLER, not by this package: rg
	// --files-with-matches and grep -l emit bare paths, which breaks the
	// path:line:text parse every backend shares. The delivery layer drops
	// the lines instead, which is where the token saving actually lands.
	FilesOnly bool
}

// scopeArgs resolves opts.Paths against root, dropping anything that escapes
// it. Returns the operands to pass to the backend (always non-empty: "." when
// nothing valid was requested) plus the rejected entries.
//
// Containment is checked on the SYMLINK-RESOLVED path, matching the native
// scanner's existing check: a path argument is attacker-reachable in an
// agent setting, and "search this directory" must not become "read /etc".
func scopeArgs(root string, paths []string) (operands []string, rejected []string) {
	// root arrives relative in CLI use ("."), and EvalSymlinks returns an
	// absolute path -- comparing the two made filepath.Rel fail and rejected
	// every legitimate scope, so a scoped search silently returned nothing.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	resolvedRoot := absRoot
	if rr, err := filepath.EvalSymlinks(absRoot); err == nil {
		resolvedRoot = rr
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(absRoot, p)
		}
		real := abs
		if rr, err := filepath.EvalSymlinks(abs); err == nil {
			real = rr
		}
		rel, err := filepath.Rel(resolvedRoot, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rejected = append(rejected, p)
			continue
		}
		operands = append(operands, "./"+filepath.ToSlash(rel))
	}
	if len(operands) == 0 && len(rejected) > 0 {
		// Every requested scope was rejected. Do NOT fall back to the whole
		// tree: the caller asked about one place, and hits from everywhere
		// else read as an answer to the question they asked. Return nothing
		// and let RejectedPaths explain why.
		return nil, rejected
	}
	if len(operands) == 0 {
		operands = []string{"."}
	}
	return operands, rejected
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

// When an agent asks for a text search it is asking for grep's semantics —
// the steering says "use it exactly as you would grep", and grep -r searches
// dotfiles. Prism does NOT add its own exclusion list beyond prism state and
// the VCS dir: .gitignore'd trees are skipped (natively by rg; approximated
// for the fallbacks by gitignoreDirs), binaries are skipped, and hidden paths
// ARE searched on every backend (rg gets --hidden) — rg's default hidden-skip
// made .github/.clinerules-style configs read as "not in the repo", which is
// silent absence, the one divergence direction this package forbids.
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
	if opts.Exhaustive {
		// Large rather than unlimited: a runaway pattern on a monorepo must
		// still terminate, and TotalHits reports what was actually seen.
		opts.MaxHits, opts.MaxPerFile = 100000, 10000
	}
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
		// Force the filename onto every line. With a SINGLE FILE operand rg
		// (and grep) omit it, emitting "988:text" instead of
		// "path:988:text" -- runLineTool parses path:line:text, so a search
		// scoped to one file silently returned zero hits. Found the moment
		// path scoping landed; it is invisible for directory operands.
		"--with-filename",
		// rg honors .gitignore only INSIDE a git repository. A worktree, an
		// unpacked tarball or a subdirectory checkout has the ignore file but
		// no .git, and rg would then search everything the project asked it
		// not to — including the venv that started all of this.
		"--no-require-git",
		// The steering promises grep semantics, and grep searches dotfiles.
		// rg's default hidden-skip made .github/workflows, .clinerules and
		// every dotfile config read as "not in the repo" — silent absence,
		// the failure mode this package's own gitignore note forbids
		// (measured: an agent concluded six hidden configs did not exist).
		// .gitignore still applies, so ignored venvs stay out.
		"--hidden",
		"--max-count", strconv.Itoa(opts.MaxPerFile),
		"--max-columns", strconv.Itoa(maxLineLen), "--max-columns-preview",
		"--max-filesize", "2M",
	}
	if !regexUsable(pattern, opts) {
		args = append(args, "--fixed-strings")
	}
	// --hidden would otherwise descend into the VCS dir and prism's state.
	args = append(args, "--glob", "!.git/", "--glob", "!.grove/")
	for _, g := range opts.Glob {
		args = append(args, "--glob", g)
	}
	operands, rejected := scopeArgs(root, opts.Paths)
	if len(operands) == 0 {
		return Result{Backend: "rg", RejectedPaths: rejected}, true
	}
	args = append(args, "-e", pattern, "--")
	args = append(args, operands...)
	r, ok := runLineTool(ctx, root, bin("rg"), args, nil, opts)
	r.RejectedPaths = rejected
	return r, ok
}

// runGrep shells out to grep, pinned to the C locale — under a UTF-8 locale
// GNU grep's multibyte path is several times slower, and byte-oriented
// matching is exactly right for fixed-string code search.
//
// Hidden paths are searched (grep's own -r semantics — the contract the
// steering promises). Backend parity with rg is kept the other way around:
// runRg passes --hidden. Only prism's state, the VCS dir, and the root
// .gitignore's plain directory names are excluded; a non-gitignored venv on
// a grep-only machine therefore surfaces as extra hits — the stated-safe
// divergence direction (never silent absence).
func runGrep(ctx context.Context, root, pattern string, opts Options) (Result, bool) {
	mode := "-F"
	if regexUsable(pattern, opts) {
		mode = "-E"
	}
	args := []string{"-rnHI", mode, "-i", "--max-count", strconv.Itoa(opts.MaxPerFile)}
	for _, d := range append(append([]string{}, excludeDirs...), gitignoreDirs(root)...) {
		args = append(args, "--exclude-dir="+d)
	}
	for _, g := range opts.Glob {
		args = append(args, "--include="+g)
	}
	operands, rejected := scopeArgs(root, opts.Paths)
	if len(operands) == 0 {
		return Result{Backend: "grep", RejectedPaths: rejected}, true
	}
	args = append(args, "-e", pattern, "--")
	args = append(args, operands...)
	r, ok := runLineTool(ctx, root, bin("grep"), args, []string{"LC_ALL=C"}, opts)
	r.RejectedPaths = rejected
	return r, ok
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
	files := map[string]bool{}
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		ln, aerr := strconv.Atoi(parts[1])
		if aerr != nil || ln < 1 {
			continue
		}
		file := filepath.ToSlash(strings.TrimPrefix(parts[0], "./"))
		// Count EVERY match, then keep the first MaxHits. The loop used to
		// break at the cap, so a truncated result reported `truncated: true`
		// with no denominator -- an agent could not tell 22-of-25 from
		// 22-of-990 (measured: "func " in this repo returns 22 hits and
		// says truncated, against 990 real occurrences). A silent narrowing
		// is the one thing this package is not allowed to do.
		res.TotalHits++
		files[file] = true
		if len(res.Hits) >= opts.MaxHits {
			res.Truncated = true
			continue
		}
		res.Hits = append(res.Hits, Hit{File: file, Line: ln, Text: truncateLine(parts[2])})
	}
	res.FilesMatched = len(files)
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
