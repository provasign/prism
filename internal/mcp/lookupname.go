package mcp

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// lookupQuery is a lookup name reduced to the index's dotted form.
//
// Agents write symbol names the way their language, docs or stack traces do:
// Rust/C++/PHP "::", PHP "->" and "\Ns\Class", Java "Outer$Inner" and
// "Type#method(int)", C# "Outer+Inner" and "Repo`1", Go "(*T).M", JS
// "X.prototype.m", call parens, signatures and generic arguments. The index
// stores dotted qualified names (C++ keeps "::", which is normalized on the
// candidate side), so every one of those forms is reduced to segments here.
type lookupQuery struct {
	raw string
	// segs are the dotted segments; the last one is the symbol name. Leading
	// segments are qualifiers: owner types, then packages/modules.
	segs []string
	// pathHint is a slash path (directory, file, file stem or Go import path)
	// the symbol must live under, from forms like "gin/render.JSON",
	// "src/flask/helpers.py:url_for" or "flask.helpers:url_for".
	pathHint string
}

func (q lookupQuery) name() string {
	if len(q.segs) == 0 {
		return ""
	}
	return q.segs[len(q.segs)-1]
}

// owner is the segment directly before the name, or "".
func (q lookupQuery) owner() string {
	if len(q.segs) < 2 {
		return ""
	}
	return q.segs[len(q.segs)-2]
}

var (
	lookupKeywordPrefix   = regexp.MustCompile(`^(?:struct|class|enum|union|interface|trait|record|object|protocol|func|fn|def|function)\s+`)
	lookupFileColonForm   = regexp.MustCompile(`^(.+\.(?:py|pyi|ts|tsx|js|jsx|mjs|cjs|go|java|kt|kts|rb|php|rs|c|h|cc|cpp|cxx|hpp|hh|hxx|cs|swift|scala|m|mm|lua|ex|exs|dart))(::|:)(.+)$`)
	lookupModuleColonForm = regexp.MustCompile(`^([A-Za-z_][\w.]*):([A-Za-z_][\w.]*)$`)
	lookupReceiverForm    = regexp.MustCompile(`(^|[.:])\(\s*[*&]?\s*([A-Za-z_][\w.:]*)\s*\)(\.|::)`)
	lookupUFCSForm        = regexp.MustCompile(`^<\s*([A-Za-z_][\w:]*)(?:<[^>]*>)?\s+as\s+[^>]+>::`)
	lookupArityForm       = regexp.MustCompile("`\\d+")
	lookupSourceExt       = regexp.MustCompile(`^(?:py|pyi|ts|tsx|js|jsx|mjs|cjs|go|java|kt|kts|rb|php|rs|c|h|cc|cpp|cxx|hpp|hh|hxx|cs|swift|scala|m|mm|lua|ex|exs|dart|groovy)$`)
)

// parseLookupName reduces a caller-supplied symbol name to a lookupQuery.
func parseLookupName(raw string) lookupQuery {
	q := lookupQuery{raw: raw}
	s := strings.TrimSpace(raw)
	s = lookupKeywordPrefix.ReplaceAllString(s, "")
	s = strings.TrimPrefix(s, "global::")

	// "path/file.py:name", "path/file.py::Class::name" (pytest node ids).
	if m := lookupFileColonForm.FindStringSubmatch(s); m != nil && !strings.Contains(m[1], "::") {
		q.pathHint = m[1]
		s = m[3]
	} else if m := lookupModuleColonForm.FindStringSubmatch(s); m != nil {
		// Python entry-point form "pkg.module:attr".
		q.pathHint = strings.ReplaceAll(m[1], ".", "/")
		s = m[2]
	}

	// Java constructor spelling "Type.<init>" names the constructor, which
	// the index records as Type.Type.
	ctor := false
	for _, suf := range []string{".<init>", "#<init>", "::<init>"} {
		if strings.HasSuffix(s, suf) {
			s = strings.TrimSuffix(s, suf)
			ctor = true
		}
	}
	// Rust fully qualified syntax "<MemStore as Store>::get".
	s = lookupUFCSForm.ReplaceAllString(s, "$1::")
	// Go method expressions "(*Context).JSON" / "pkg.(*T).M".
	s = lookupReceiverForm.ReplaceAllString(s, "$1$2$3")
	// PHP static/instance property access "Class::$prop" / "$obj->prop".
	s = strings.ReplaceAll(s, "::$", "::")
	s = strings.ReplaceAll(s, "->$", "->")
	s = strings.ReplaceAll(s, "->", ".")
	// A call or signature suffix: "foo()", "T.m(int, String)", "get(key:)",
	// "area() const". Cut at the first top-level '(' (operator() excepted).
	if i := strings.IndexByte(s, '('); i > 0 && !strings.Contains(s[:i], "operator") {
		s = s[:i]
	}
	s = stripBalanced(s, '<', '>')
	s = stripBalanced(s, '[', ']')
	s = lookupArityForm.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, ".prototype.", ".")
	s = strings.TrimSuffix(s, ".prototype")
	s = strings.ReplaceAll(s, "::", ".")
	s = strings.ReplaceAll(s, "#", ".")
	s = strings.ReplaceAll(s, `\`, ".")
	s = replaceBetweenWordChars(s, '$', '.')
	if !strings.Contains(s, "operator") {
		s = replaceBetweenWordChars(s, '+', '.')
	}
	s = strings.TrimSpace(s)

	// Path forms: everything up to the last '/' plus the first dotted segment
	// after it (or "stem.ext" when that segment is a source file) is a path.
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		dirPart, rest := s[:i], s[i+1:]
		dotted := splitLookupSegs(rest)
		pathPart := dirPart
		consumed := 0
		if len(dotted) >= 2 && lookupSourceExt.MatchString(dotted[1]) {
			pathPart += "/" + dotted[0] + "." + dotted[1]
			consumed = 2
		} else if len(dotted) >= 1 {
			pathPart += "/" + dotted[0]
			consumed = 1
		}
		pathPart = strings.TrimPrefix(strings.TrimPrefix(pathPart, "./"), "/")
		segs := dotted[consumed:]
		if len(segs) == 0 && len(dotted) > 0 {
			// The whole name is a path: "src/x/Streams.java" or
			// "./relation-id/RelationIdLoader" names the file's main symbol.
			segs = []string{dotted[0]}
		}
		if q.pathHint == "" {
			q.pathHint = pathPart
		} else {
			q.pathHint = pathPart + "/" + q.pathHint
		}
		q.segs = segs
	} else {
		q.segs = splitLookupSegs(s)
		// "value.c.json_object_get", "jansson.h.json_object_get": a leading
		// file name selects the file.
		if len(q.segs) >= 3 && lookupSourceExt.MatchString(q.segs[1]) && q.pathHint == "" {
			q.pathHint = q.segs[0] + "." + q.segs[1]
			q.segs = q.segs[2:]
		}
	}
	q.pathHint = strings.TrimSuffix(strings.TrimPrefix(q.pathHint, "./"), "/")

	// Rust path roots and Kotlin companion objects carry no identity.
	for len(q.segs) > 1 && (q.segs[0] == "crate" || q.segs[0] == "self" || q.segs[0] == "super") {
		q.segs = q.segs[1:]
	}
	kept := q.segs[:0:0]
	for i, seg := range q.segs {
		if seg == "Companion" && i < len(q.segs)-1 {
			continue
		}
		kept = append(kept, seg)
	}
	q.segs = kept
	if ctor && len(q.segs) > 0 {
		q.segs = append(q.segs, q.segs[len(q.segs)-1])
	}
	if len(q.segs) == 0 && raw != "" {
		q.segs = []string{strings.TrimSpace(raw)}
	}
	return q
}

// splitLookupSegs splits a dotted name, dropping empty segments and quotes
// (TypeScript records quoted members as `DataSource."@instanceof"`).
func splitLookupSegs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ".") {
		p = strings.Trim(strings.TrimSpace(p), `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// symbolSegs is a candidate's qualified name as segments, with C++ "::"
// normalized to the dotted form and generic arguments removed.
func symbolSegs(s grove.SymbolRecord) []string {
	qn := s.QualifiedName
	if qn == "" {
		qn = s.Name
	}
	qn = strings.ReplaceAll(qn, "::", ".")
	// Generic arguments only: "Repo<T>", "List[T]". A segment that is itself
	// bracketed ("ObjectMapper.<anonymous@908:28>", "<top-level>") is a
	// name and must survive, or it would pose as its enclosing type.
	qn = stripGenericArgs(qn, '<', '>')
	qn = stripGenericArgs(qn, '[', ']')
	segs := splitLookupSegs(qn)
	if len(segs) == 0 {
		segs = []string{s.Name}
	}
	return segs
}

// stripBalanced removes every balanced open…close group (nesting allowed).
// An unbalanced close is kept as-is.
func stripBalanced(s string, open, close byte) string {
	if strings.IndexByte(s, open) < 0 {
		return s
	}
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == open:
			depth++
		case c == close && depth > 0:
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// stripGenericArgs removes balanced open…close groups that directly follow
// an identifier character.
func stripGenericArgs(s string, open, close byte) string {
	if strings.IndexByte(s, open) < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == open && i > 0 && isLookupWordChar(s[i-1]) {
			depth, j := 0, i
			for ; j < len(s); j++ {
				if s[j] == open {
					depth++
				} else if s[j] == close {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j < len(s) {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isLookupWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// replaceBetweenWordChars replaces sep with repl only where it joins two
// identifier characters ("Outer$Inner", "Cart+Line"), so a leading "$scope"
// stays an identifier.
func replaceBetweenWordChars(s string, sep, repl byte) string {
	if strings.IndexByte(s, sep) < 0 {
		return s
	}
	b := []byte(s)
	for i := 1; i < len(b)-1; i++ {
		if b[i] == sep && isLookupWordChar(b[i-1]) && isLookupWordChar(b[i+1]) {
			b[i] = repl
		}
	}
	return string(b)
}

func lookupEq(a, b string, fold bool) bool {
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// segsHaveSuffix reports whether full ends with suffix.
func segsHaveSuffix(full, suffix []string, fold bool) bool {
	if len(suffix) > len(full) {
		return false
	}
	off := len(full) - len(suffix)
	for i := range suffix {
		if !lookupEq(full[off+i], suffix[i], fold) {
			return false
		}
	}
	return true
}

// lookupLocator answers "does qualifier X describe where symbol S lives?"
// for one lookup call: package clauses and namespaces are read from file
// headers once and cached.
type lookupLocator struct {
	root       string
	rootBase   string
	namespaces map[string][]string
	goModule   *string
}

func newLookupLocator(root string) *lookupLocator {
	return &lookupLocator{root: root, rootBase: filepath.Base(filepath.Clean(root)), namespaces: map[string][]string{}}
}

var (
	lookupGoPackage   = regexp.MustCompile(`^\s*package\s+([A-Za-z_]\w*)`)
	lookupJvmPackage  = regexp.MustCompile(`^\s*package\s+([A-Za-z_][\w.]*)`)
	lookupCsNamespace = regexp.MustCompile(`^\s*namespace\s+([A-Za-z_][\w.]*)`)
	lookupPhpNS       = regexp.MustCompile(`^\s*namespace\s+([A-Za-z_][\w\\]*)\s*[;{]`)
)

// namespace returns the file's declared package/namespace as segments: Go
// "package gin", Java/Kotlin/Scala "package a.b", C# "namespace A.B", PHP
// "namespace A\B;". Languages without one return nil.
func (l *lookupLocator) namespace(file string) []string {
	if ns, ok := l.namespaces[file]; ok {
		return ns
	}
	var re *regexp.Regexp
	switch strings.ToLower(path.Ext(file)) {
	case ".go":
		re = lookupGoPackage
	case ".java", ".kt", ".kts", ".scala", ".groovy":
		re = lookupJvmPackage
	case ".cs":
		re = lookupCsNamespace
	case ".php":
		re = lookupPhpNS
	}
	var ns []string
	if re != nil {
		if f, err := os.Open(filepath.Join(l.root, filepath.FromSlash(file))); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for n := 0; n < 400 && sc.Scan(); n++ {
				if m := re.FindStringSubmatch(sc.Text()); m != nil {
					ns = splitLookupSegs(strings.ReplaceAll(m[1], `\`, "."))
					break
				}
			}
			f.Close()
		}
	}
	l.namespaces[file] = ns
	return ns
}

// goModulePath is the root go.mod's module path, or "".
func (l *lookupLocator) goModulePath() string {
	if l.goModule != nil {
		return *l.goModule
	}
	mod := ""
	if data, err := os.ReadFile(filepath.Join(l.root, "go.mod")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
				mod = strings.Trim(f[1], `"`)
				break
			}
		}
	}
	l.goModule = &mod
	return mod
}

// fileParts splits a repo-relative file into directory segments and the
// file stem. Package-marker files (__init__.py, index.ts, mod.rs) have no
// stem of their own: the directory is the module.
func fileParts(file string) (dirSegs []string, stem string) {
	file = filepath.ToSlash(file)
	dir, base := path.Split(file)
	for _, d := range strings.Split(strings.Trim(dir, "/"), "/") {
		if d != "" && d != "." {
			dirSegs = append(dirSegs, d)
		}
	}
	stem = strings.TrimSuffix(base, path.Ext(base))
	switch stem {
	case "__init__", "index", "mod":
		stem = ""
	}
	return dirSegs, stem
}

// qualifierMatches reports whether the dotted qualifier P (packages/modules,
// not owner types) describes where file lives: a suffix of the declared
// package/namespace, of the directory path, or of directory+file stem (Python
// modules, JS files, a Java class's own file). The repository's own name may
// lead ("gin.Default", "typeorm.DataSource"). Case-sensitive unless fold.
func (l *lookupLocator) qualifierMatches(p []string, file string, fold bool) bool {
	if len(p) == 0 {
		return true
	}
	if lookupEq(p[0], l.rootBase, fold) && (len(p) == 1 || l.qualifierMatches(p[1:], file, fold)) {
		return true
	}
	if ns := l.namespace(file); len(ns) > 0 && segsHaveSuffix(ns, p, fold) {
		return true
	}
	dirSegs, stem := fileParts(file)
	if len(dirSegs) > 0 && segsHaveSuffix(dirSegs, p, fold) {
		return true
	}
	if stem != "" {
		withStem := append(append([]string(nil), dirSegs...), stem)
		if segsHaveSuffix(withStem, p, fold) {
			return true
		}
		// Kotlin top-level functions compile into <Stem>Kt.
		if len(p) == 1 && lookupEq(p[0], stem+"Kt", fold) {
			return true
		}
	}
	return false
}

// pathMatches reports whether a slash path hint names file, its stem, its
// directory, or (Go) its import path.
func (l *lookupLocator) pathMatches(hint, file string, fold bool) bool {
	if hint == "" {
		return true
	}
	file = filepath.ToSlash(file)
	if fold {
		hint, file = strings.ToLower(hint), strings.ToLower(file)
	}
	stem := strings.TrimSuffix(file, path.Ext(file))
	dir := path.Dir(file)
	if dir == "." {
		dir = ""
	}
	suffix := func(a, b string) bool { return a != "" && b != "" && (a == b || strings.HasSuffix(a, "/"+b)) }
	if suffix(file, hint) || suffix(stem, hint) || suffix(dir, hint) || suffix(hint, dir) {
		return true
	}
	switch path.Base(stem) {
	case "__init__", "index", "mod":
		if suffix(dir, hint) {
			return true
		}
	}
	if strings.HasSuffix(file, ".go") {
		if mod := l.goModulePath(); mod != "" {
			if fold {
				mod = strings.ToLower(mod)
			}
			importPath := mod
			if dir != "" {
				importPath = mod + "/" + dir
			}
			if importPath == hint || suffix(importPath, hint) {
				return true
			}
		}
	}
	return false
}

// lookupMatch scores symbol s against q. ok=false with conflict=true means
// s has the requested name but a qualifier contradicts it (another owner,
// package, module or path) — it must never be returned as an exact match.
func (l *lookupLocator) lookupMatch(q lookupQuery, s grove.SymbolRecord, fold bool) (score int, ok, conflict bool) {
	name := q.name()
	n := symbolSegs(s)
	if !lookupEq(n[len(n)-1], name, fold) && !lookupEq(s.Name, name, fold) {
		return 0, false, false
	}
	if !lookupEq(n[len(n)-1], name, fold) {
		// The record's QN ends differently from its Name; compare on Name.
		n = append(append([]string(nil), n[:len(n)-1]...), s.Name)
	}
	// Locations stay case-sensitive even in the case-insensitive pass:
	// "Flask.jsonify" must not reach src/flask/json via the folded
	// directory name "flask".
	if q.pathHint != "" && !l.pathMatches(q.pathHint, s.FilePath, false) {
		return 0, false, true
	}
	m := len(q.segs)
	for k := 0; k < m; k++ {
		qs := q.segs[k:]
		if !segsHaveSuffix(n, qs, fold) {
			continue
		}
		if k > 0 && !l.qualifierMatches(q.segs[:k], s.FilePath, false) {
			conflict = true
			continue
		}
		score = 100*len(qs) + 40*k
		if len(qs) == len(n) {
			score += 20
			// An owner-less symbol in a file named after the qualifier
			// ("serializer.dump" -> dump in serializer.hpp, "helpers.url_for")
			// is that type's/module's member; the index just lost the owner
			// (C++ templates). Rank it like an owner match.
			if k == 1 && len(n) == 1 && q.segs[0] != name {
				if _, stem := fileParts(s.FilePath); stem == q.segs[0] {
					score += 60
				}
			}
		}
		if q.pathHint != "" {
			score += 30
		}
		if isTestDouble(s.FilePath) {
			score -= 10
		}
		// A bare "Circle" means the class, not its constructor.
		if s.Kind == "constructor" && !(m >= 2 && lookupEq(q.segs[m-2], name, fold)) {
			score -= 5
		}
		return score, true, false
	}
	return 0, false, true
}

// isTypeKind reports kinds that can own members and take part in
// extends/implements edges.
func isTypeKind(kind string) bool {
	switch kind {
	case "class", "interface", "struct", "trait", "type", "enum":
		return true
	}
	return false
}

// osaDistance is the optimal-string-alignment edit distance (Levenshtein
// plus adjacent transposition), case-insensitive.
func osaDistance(a, b string) int {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	la, lb := len(ra), len(rb)
	d := make([][]int, la+1)
	for i := range d {
		d[i] = make([]int, lb+1)
		d[i][0] = i
	}
	for j := 0; j <= lb; j++ {
		d[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			v := min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				v = min(v, d[i-2][j-2]+1)
			}
			d[i][j] = v
		}
	}
	return d[la][lb]
}

// typoThreshold is the largest edit distance still offered as a suggestion.
func typoThreshold(name string) int {
	switch n := len([]rune(name)); {
	case n <= 4:
		return 1
	case n <= 8:
		return 2
	default:
		return 3
	}
}

// typoProbes are substrings of term that survive a single edit in at least
// one probe: consecutive non-overlapping chunks plus the tail.
func typoProbes(term string) []string {
	r := []rune(term)
	if len(r) < 3 {
		return nil
	}
	g := 3
	if len(r) < 6 {
		g = 2
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	for i := 0; i+g <= len(r); i += g {
		add(string(r[i : i+g]))
	}
	add(string(r[len(r)-g:]))
	return out
}
