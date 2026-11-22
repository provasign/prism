package mcp

import (
	"bufio"
	"database/sql"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// planChange is one changed symbol's line span on each side of the diff,
// for matching recorded coverage. A nil side does not exist there.
type planChange struct {
	File          string
	Name          string
	Before, After *[2]int
}

// planCoverage is what one recorded coverage file says about the change.
// Coverage-based mapping is safer than the static graph for the lines it
// covers (MKT-08); each selection says which source produced it.
type planCoverage struct {
	Source string `json:"source"`
	Format string `json:"format"`
	// PerTest: the file records which test ran each line (lcov TN:,
	// coverage.py contexts). Otherwise it only says whether the suite did.
	PerTest bool `json:"perTest"`
	// Matched: "base" when the file is older than the edited files (its
	// line numbers are the base revision's), else "current".
	Matched string              `json:"matched"`
	Symbols []planCoveredSymbol `json:"symbols"`
}

type planCoveredSymbol struct {
	Symbol string `json:"symbol"`
	File   string `json:"file"`
	// Instrumented is -1 when the format records executed lines only.
	Instrumented int      `json:"instrumented"`
	Executed     int      `json:"executed"`
	Tests        []string `json:"tests,omitempty"`
}

// planCoverageCandidates are the files read when no --coverage is given.
var planCoverageCandidates = []string{
	"coverage.out", "cover.out", "coverage.txt", "profile.cov",
	"lcov.info", "coverage/lcov.info",
	".coverage",
	"target/site/jacoco/jacoco.xml", "build/reports/jacoco/test/jacocoTestReport.xml",
}

// lineCov is one source line in a coverage file.
type lineCov struct {
	instrumented bool
	count        int
	tests        map[string]bool
}

type fileCov map[int]*lineCov

func (f fileCov) mark(line, count int, test string) {
	lc := f[line]
	if lc == nil {
		lc = &lineCov{}
		f[line] = lc
	}
	lc.instrumented = true
	lc.count += count
	if count > 0 && test != "" {
		if lc.tests == nil {
			lc.tests = map[string]bool{}
		}
		lc.tests[test] = true
	}
}

// planCoverageFiles resolves the coverage files to read: the ones asked
// for, else the conventional names that exist.
func planCoverageFiles(root string, asked []string) ([]string, []string) {
	var out, gaps []string
	if len(asked) > 0 {
		for _, p := range asked {
			abs := p
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(root, p)
			}
			if _, err := os.Stat(abs); err != nil {
				gaps = append(gaps, "coverage file not readable: "+p)
				continue
			}
			out = append(out, abs)
		}
		return out, gaps
	}
	for _, c := range planCoverageCandidates {
		abs := filepath.Join(root, filepath.FromSlash(c))
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			out = append(out, abs)
		}
	}
	return out, gaps
}

// planReadCoverage reads one coverage file, keeping only the records for
// the changed files (matched by path suffix, so module prefixes and
// absolute paths line up with repository paths).
func planReadCoverage(root, path string, changed map[string]bool) (format string, perTest bool, cov map[string]fileCov, err error) {
	cov = map[string]fileCov{}
	match := func(p string) string {
		p = filepath.ToSlash(p)
		if rel, rerr := filepath.Rel(root, p); rerr == nil && !strings.HasPrefix(rel, "..") && filepath.IsAbs(p) {
			p = filepath.ToSlash(rel)
		}
		for f := range changed {
			// Go profiles prefix the module path; JaCoCo and some lcov
			// writers drop the source root (src/main/java/...).
			if p == f || strings.HasSuffix(p, "/"+f) || strings.HasSuffix(f, "/"+p) {
				return f
			}
		}
		return ""
	}
	get := func(f string) fileCov {
		if cov[f] == nil {
			cov[f] = fileCov{}
		}
		return cov[f]
	}
	base := filepath.Base(path)
	switch {
	case base == ".coverage":
		perTest, err = readCoveragePy(path, match, get)
		return "coverage.py", perTest, cov, err
	case strings.HasSuffix(base, ".xml"):
		return "jacoco", false, cov, readJaCoCo(path, match, get)
	}
	fh, err := os.Open(path)
	if err != nil {
		return "", false, nil, err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	if !sc.Scan() {
		return "", false, cov, nil
	}
	first := sc.Text()
	if strings.HasPrefix(first, "mode:") {
		// Go: file.go:startLine.startCol,endLine.endCol numStmts count
		for sc.Scan() {
			line := sc.Text()
			colon := strings.LastIndex(line, ".go:")
			if colon < 0 {
				continue
			}
			f := match(line[:colon+3])
			if f == "" {
				continue
			}
			var sl, sCol, el, eCol, n, count int
			if _, e := fmt.Sscanf(line[colon+4:], "%d.%d,%d.%d %d %d", &sl, &sCol, &el, &eCol, &n, &count); e != nil {
				continue
			}
			for l := sl; l <= el; l++ {
				get(f).mark(l, count, "")
			}
		}
		return "go", false, cov, sc.Err()
	}
	// lcov: TN:<test> / SF:<file> / DA:<line>,<count> / end_of_record
	test, cur := "", ""
	tests := map[string]bool{}
	handle := func(l string) {
		switch {
		case strings.HasPrefix(l, "TN:"):
			test = strings.TrimSpace(l[3:])
			if test != "" {
				tests[test] = true
			}
		case strings.HasPrefix(l, "SF:"):
			cur = match(strings.TrimSpace(l[3:]))
		case strings.HasPrefix(l, "DA:") && cur != "":
			parts := strings.Split(l[3:], ",")
			if len(parts) >= 2 {
				ln, e1 := strconv.Atoi(parts[0])
				cnt, e2 := strconv.Atoi(parts[1])
				if e1 == nil && e2 == nil {
					get(cur).mark(ln, cnt, test)
				}
			}
		case l == "end_of_record":
			cur = ""
		}
	}
	handle(first)
	for sc.Scan() {
		handle(sc.Text())
	}
	return "lcov", len(tests) > 1, cov, sc.Err()
}

func readJaCoCo(path string, match func(string) string, get func(string) fileCov) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	dec := xml.NewDecoder(fh)
	dec.Strict = false
	pkg, cur := "", ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil // io.EOF or a truncated report: keep what was read
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := func(name string) string {
			for _, a := range se.Attr {
				if a.Name.Local == name {
					return a.Value
				}
			}
			return ""
		}
		switch se.Name.Local {
		case "package":
			pkg = attr("name")
		case "sourcefile":
			cur = match(pkg + "/" + attr("name"))
		case "line":
			if cur == "" {
				continue
			}
			nr, _ := strconv.Atoi(attr("nr"))
			ci, _ := strconv.Atoi(attr("ci"))
			if nr > 0 {
				get(cur).mark(nr, ci, "")
			}
		}
	}
}

// readCoveragePy reads a coverage.py data file (SQLite, coverage 5+).
// Executed lines only; with dynamic contexts (pytest --cov-context=test)
// each context names the test that ran the line.
func readCoveragePy(path string, match func(string) string, get func(string) fileCov) (bool, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return false, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT f.path, c.context, lb.numbits FROM line_bits lb
		JOIN file f ON f.id = lb.file_id JOIN context c ON c.id = lb.context_id`)
	if err != nil {
		return false, fmt.Errorf("coverage.py data (line coverage, coverage 5+ expected): %w", err)
	}
	defer rows.Close()
	contexts := map[string]bool{}
	for rows.Next() {
		var p, ctx string
		var bits []byte
		if err := rows.Scan(&p, &ctx, &bits); err != nil {
			return false, err
		}
		f := match(p)
		if f == "" {
			continue
		}
		test := strings.TrimSuffix(strings.TrimSuffix(ctx, "|run"), "|setup")
		if test != "" {
			contexts[test] = true
		}
		for i, b := range bits {
			for k := 0; k < 8; k++ {
				if b&(1<<k) != 0 {
					get(f).mark(i*8+k, 1, test)
				}
			}
		}
	}
	return len(contexts) > 0, rows.Err()
}

// planCoverageReport matches every coverage file against the changed
// symbols. It returns the per-file reports and the test files coverage
// ties to a changed line.
func planCoverageReport(root string, asked []string, changes []planChange, testFileOf func(string) string) ([]planCoverage, map[string][]string, []string) {
	files, gaps := planCoverageFiles(root, asked)
	if len(files) == 0 || len(changes) == 0 {
		return nil, nil, gaps
	}
	changed := map[string]bool{}
	newest := map[string]int64{}
	for _, c := range changes {
		changed[c.File] = true
		if fi, err := os.Stat(filepath.Join(root, c.File)); err == nil {
			newest[c.File] = fi.ModTime().UnixNano()
		}
	}
	var out []planCoverage
	testFiles := map[string][]string{}
	for _, path := range files {
		format, perTest, cov, err := planReadCoverage(root, path, changed)
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if err != nil {
			gaps = append(gaps, fmt.Sprintf("coverage file %s not read: %v", rel, err))
			continue
		}
		recorded := int64(0)
		if fi, err := os.Stat(path); err == nil {
			recorded = fi.ModTime().UnixNano()
		}
		pc := planCoverage{Source: rel, Format: format, PerTest: perTest, Matched: "current", Symbols: []planCoveredSymbol{}}
		for _, c := range changes {
			span := c.After
			if recorded < newest[c.File] && c.Before != nil {
				// Recorded before the edit: its line numbers are the base's.
				span, pc.Matched = c.Before, "base"
			}
			if span == nil {
				continue
			}
			cs := planCoveredSymbol{Symbol: c.Name, File: c.File, Instrumented: 0}
			if format == "coverage.py" {
				cs.Instrumented = -1
			}
			tests := map[string]bool{}
			for l := span[0]; l <= span[1]; l++ {
				lc := cov[c.File][l]
				if lc == nil {
					continue
				}
				if cs.Instrumented >= 0 && lc.instrumented {
					cs.Instrumented++
				}
				if lc.count > 0 {
					cs.Executed++
				}
				for t := range lc.tests {
					tests[t] = true
				}
			}
			for t := range tests {
				cs.Tests = append(cs.Tests, t)
				if f := testFileOf(t); f != "" {
					testFiles[f] = append(testFiles[f], rel)
				}
			}
			sort.Strings(cs.Tests)
			pc.Symbols = append(pc.Symbols, cs)
		}
		out = append(out, pc)
	}
	return out, testFiles, gaps
}

// planApplyCoverage attaches the coverage reports and adds the test files
// per-test coverage ties to a changed line to the run-first tier.
func planApplyCoverage(r *testPlanResult, root string, asked []string, syms []grove.SymbolRecord) {
	known := map[string]bool{}
	for _, s := range syms {
		known[normalizePath(s.FilePath)] = true
	}
	cov, testFiles, gaps := planCoverageReport(root, asked, r.changes, func(t string) string { return coverageTestFile(t, known) })
	r.Coverage = cov
	r.Gaps = append(r.Gaps, gaps...)
	if len(testFiles) == 0 {
		return
	}
	idx := map[string]int{}
	for i, t := range r.FileTargets {
		idx[t.File] = i
	}
	files := make([]string, 0, len(testFiles))
	for f := range testFiles {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if i, ok := idx[f]; ok {
			r.FileTargets[i].Sources = []string{"graph", "coverage"}
			continue
		}
		r.FileTargets = append(r.FileTargets, testPlanTarget{
			File: f, Classification: "coverage file", Origins: []string{}, Evidence: []testPlanStep{},
			Sources: []string{"coverage"},
			Reason:  "recorded coverage (" + strings.Join(uniqueSorted(testFiles[f]), ", ") + ") has a test in this file running a changed line; the static graph did not reach it",
		})
	}
}

// coverageTestFile maps a recorded test name to a repository file:
// "tests/test_x.py::test_y" (pytest node id) by its path, and
// "pkg.test_x.test_y" (coverage.py test_function context) by the longest
// dotted prefix that names a known .py file. "" when nothing matches.
func coverageTestFile(name string, known map[string]bool) string {
	if f, _, ok := strings.Cut(name, "::"); ok {
		return normalizePath(f)
	}
	parts := strings.Split(name, ".")
	for k := len(parts) - 1; k >= 1; k-- {
		suffix := strings.Join(parts[:k], "/") + ".py"
		var hit string
		for f := range known {
			if f == suffix || strings.HasSuffix(f, "/"+suffix) {
				if hit != "" {
					return "" // ambiguous
				}
				hit = f
			}
		}
		if hit != "" {
			return hit
		}
	}
	return ""
}
