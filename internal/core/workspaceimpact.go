package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/linkserver"
	"github.com/provasign/prism/internal/textsearch"
	"github.com/provasign/prism/internal/workspace"
)

// A change to a symbol reaches other repositories in ways Prism can prove to
// different degrees, and an answer must never read stronger than its proof:
//
//   - resolved: the other repository's compiler pass recorded a reference to
//     the symbol's key. These rows are sites.
//   - name-matched: the compiler pass did not run there, there is no index,
//     or this kind of symbol has no cross-repository records. Prism falls
//     back to whole-word text matches, narrowed to files that also mention
//     the owner type or the package, and labels every such row. Silence here
//     would read as "no consumers".
//   - not searched: not checked out, link not confirmed, server unreachable.
//
// Every answer says which of these applied to which repository.

// linkTarget is one symbol of a change set, as other repositories may know it.
type linkTarget struct {
	Keys     []string // cross-repository keys; empty when this kind has none
	Name     string   // the identifier at a use site
	Owner    string   // enclosing type, "" for a top-level symbol
	Language string
	Kind     string
}

func targetFor(root, language, kind, file, qualified string) linkTarget {
	segs := strings.Split(qualified, ".")
	t := linkTarget{Language: language, Kind: kind, Name: segs[len(segs)-1],
		Keys: grove.KeysFor(root, language, kind, file, qualified)}
	if len(segs) >= 2 {
		t.Owner = segs[len(segs)-2]
	}
	if t.Language == "" {
		t.Language = languageOfFile(file)
	}
	if kind == "constructor" && len(segs) >= 2 {
		t.Name, t.Owner = segs[len(segs)-2], ""
	}
	return t
}

func languageOfFile(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go":
		return "go"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".cs":
		return "csharp"
	case ".php":
		return "php"
	case ".rb":
		return "ruby"
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".cxx":
		return "cpp"
	case ".swift":
		return "swift"
	case ".scala":
		return "scala"
	}
	return ""
}

// languageGlobs: the files a consumer of a symbol in this language is
// written in. JVM and JavaScript-family languages call each other.
func languageGlobs(language string) []string {
	switch language {
	case "go":
		return []string{"*.go"}
	case "java", "kotlin", "scala":
		return []string{"*.java", "*.kt", "*.kts", "*.scala", "*.groovy"}
	case "typescript", "tsx", "javascript":
		return []string{"*.ts", "*.tsx", "*.mts", "*.cts", "*.js", "*.jsx", "*.mjs", "*.cjs", "*.vue", "*.svelte"}
	case "python":
		return []string{"*.py"}
	case "rust":
		return []string{"*.rs"}
	case "csharp":
		return []string{"*.cs"}
	case "php":
		return []string{"*.php"}
	case "ruby":
		return []string{"*.rb"}
	case "cpp", "c":
		return []string{"*.c", "*.h", "*.cc", "*.cpp", "*.hpp", "*.cxx"}
	case "swift":
		return []string{"*.swift"}
	}
	return nil
}

// keyAnalyzer names the compiler pass that writes a key's records, or ""
// for records that need none (Python's import-scoped ones).
func keyAnalyzer(key string) string {
	switch {
	case strings.HasPrefix(key, "go:"):
		return "go"
	case strings.HasPrefix(key, "jvm:"):
		return "java"
	case strings.HasPrefix(key, "npm:"):
		return "js-ts"
	}
	return ""
}

func (h *Handler) targetsOf(syms []grove.SymbolRecord) []linkTarget {
	seen := map[string]bool{}
	var out []linkTarget
	for _, s := range syms {
		id := s.Language + "\x00" + s.FilePath + "\x00" + s.QualifiedName + "\x00" + s.Kind
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, targetFor(h.Root, s.Language, s.Kind, s.FilePath, s.QualifiedName))
	}
	return out
}

// workspaceConfigured reports whether anything beyond this repository can be
// consulted or was declared.
func (h *Handler) workspaceConfigured() bool {
	return (h.Workspace != nil && len(h.Workspace.Roots) > 0) || (h.Cfg != nil && h.Cfg.WorkspaceServer != "")
}

// addWorkspaceConsumers extends a change-impact answer past this repository
// and corrects the repository-scoped claims above it: a "test-only" label,
// a completeness claim and a boundary sentence written for one repository
// are wrong once a consumer in another one is on the page.
func (h *Handler) addWorkspaceConsumers(ctx context.Context, out map[string]any, r *grove.ChangeImpactResult) {
	if r == nil || !h.workspaceConfigured() {
		return
	}
	var syms []grove.SymbolRecord
	for _, group := range [][]grove.SymbolRecord{r.Declarations, r.Supers, r.Family} {
		syms = append(syms, group...)
	}
	ans := h.linkedAnswer(ctx, h.targetsOf(syms))
	ans.attach(out)
	if ans.sites > 0 {
		out["linkedSites"] = ans.sites
	}
	if ans.production > 0 {
		// Production code in another repository calls it.
		delete(out, "testOnly")
	} else if lines := anySlice(out["testOnly"]); len(lines) > 0 {
		out["testOnly"] = append([]any{"(in this repository; linked repositories are reported separately)"}, lines...)
	}
	if b, _ := out["scopeBoundary"].(string); b != "" {
		out["scopeBoundary"] = strings.Replace(b, "consumers outside this repository",
			"repositories that are not linked (linked ones are reported under workspaceScope)", 1)
	}
	if ans.incomplete {
		// Something linked could not be read with compiler evidence.
		if safe, _ := out["safeToClaimComplete"].(bool); safe {
			out["safeToClaimComplete"] = false
			out["completenessScope"] = fmt.Sprint(out["completenessScope"]) + "; linked repositories: NOT all resolved, see workspaceScope"
		}
	}
}

// linkedAnswer is what the other repositories say about a change set.
// LinkedSite is one site in another repository, as structured data for
// the graph explorer and JSON clients (the text lines say the same).
type LinkedSite struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col,omitempty"`
	Text     string `json:"text,omitempty"`
	From     string `json:"from,omitempty"`   // enclosing symbol
	Evidence string `json:"evidence"`         // resolved | implements | import-scoped | name-match
	Detail   string `json:"detail,omitempty"` // the interface, for implements
	Test     bool   `json:"test,omitempty"`
}

// LinkedRepo is one linked repository's (or uploaded repository's) answer.
type LinkedRepo struct {
	ID     string       `json:"id"`
	Path   string       `json:"path,omitempty"`   // display path of a checkout or clone
	Source string       `json:"source"`           // checkout | clone | server
	Commit string       `json:"commit,omitempty"` // server: the uploaded commit
	Ref    string       `json:"ref,omitempty"`
	Pin    string       `json:"pin,omitempty"`
	State  string       `json:"state"` // resolved | name-matched | unread
	Note   string       `json:"note,omitempty"`
	Sites  []LinkedSite `json:"sites"`
}

type linkedAnswer struct {
	repos      []LinkedRepo
	lines      []string
	scope      string
	sites      int  // resolved sites
	production int  // resolved sites outside test files
	matches    int  // name-matched rows
	incomplete bool // some linked repository was not read with compiler evidence
}

func (a linkedAnswer) attach(out map[string]any) {
	if len(a.lines) > 0 {
		out["workspaceConsumers"] = a.lines
	}
	if len(a.repos) > 0 {
		// Plain JSON values, so every consumer of the answer (the MCP
		// server, the graph explorer) reads the same shape.
		var plain []any
		if data, err := json.Marshal(a.repos); err == nil && json.Unmarshal(data, &plain) == nil {
			out["linkedRepos"] = plain
		}
	}
	if a.scope != "" {
		out["workspaceScope"] = a.scope
	}
}

type rootAnswer struct {
	repo       *LinkedRepo
	lines      []string
	sites      int
	production int
	matches    int
	state      string // resolved | name-matched | skipped | unread
	stale      bool
	why        string // for name-matched: why not resolved
}

// linkedAnswer consults every linked repository, concurrently, and the link
// server when one is configured.
func (h *Handler) linkedAnswer(ctx context.Context, targets []linkTarget) linkedAnswer {
	var ans linkedAnswer
	if !h.workspaceConfigured() {
		return ans
	}
	var absent []string
	var linked []workspace.Root
	if h.Workspace != nil {
		linked = h.Workspace.Linked()
		for _, r := range h.Workspace.Roots {
			if why := r.Unusable(); why != "" {
				absent = append(absent, r.ID)
				ans.lines = append(ans.lines, fmt.Sprintf("// repo %s  %s  NOT SEARCHED: %s", r.ID, h.Workspace.DisplayPath(r), why))
				ans.incomplete = true
			}
		}
	}
	var keys []string
	seen := map[string]bool{}
	for _, t := range targets {
		for _, k := range t.Keys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)

	results := make([]rootAnswer, len(linked))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, root := range linked {
		wg.Add(1)
		go func(i int, root workspace.Root) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = h.rootConsumers(ctx, root, targets, keys)
		}(i, root)
	}
	wg.Wait()

	// Checkouts the agent can edit first; read-only cache clones after, under
	// their own heading, so a fix is never made in a clone nothing builds.
	var clones []string
	resolved, skipped := 0, 0
	var matched, unread, stale []string
	for i, res := range results {
		root := linked[i]
		if root.Cache {
			clones = append(clones, res.lines...)
		} else {
			ans.lines = append(ans.lines, res.lines...)
		}
		switch {
		case res.repo != nil:
			ans.repos = append(ans.repos, *res.repo)
		case res.state == "unread":
			src := "checkout"
			if root.Cache {
				src = "clone"
			}
			note := ""
			if len(res.lines) > 0 {
				note = strings.TrimSpace(res.lines[0])
			}
			ans.repos = append(ans.repos, LinkedRepo{ID: root.ID, Path: h.Workspace.DisplayPath(root), Source: src, State: "unread", Note: note, Sites: []LinkedSite{}})
		}
		ans.sites += res.sites
		ans.production += res.production
		ans.matches += res.matches
		if res.stale {
			stale = append(stale, root.ID)
			ans.incomplete = true
		}
		switch res.state {
		case "resolved":
			resolved++
		case "skipped":
			skipped++
		case "name-matched":
			matched = append(matched, root.ID)
			ans.incomplete = true
		case "unread":
			unread = append(unread, root.ID)
			ans.incomplete = true
		}
	}
	if len(clones) > 0 {
		ans.lines = append(ans.lines, "// cache clones below are READ-ONLY reference source at a fixed commit: do not edit there, the change belongs in that repository")
		ans.lines = append(ans.lines, clones...)
	}

	total := len(linked) + len(absent)
	var parts []string
	if total > 0 {
		parts = append(parts, fmt.Sprintf("%d resolved site(s) in %d of %d linked repositories", ans.sites, resolved, total))
		if len(matched) > 0 {
			parts = append(parts, fmt.Sprintf("NAME-MATCHED ONLY, not resolved (%d line(s)): %s", ans.matches, strings.Join(matched, ", ")))
		}
		if skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped (their manifests do not require it)", skipped))
		}
		if len(stale) > 0 {
			parts = append(parts, "answered from a STALE index: "+strings.Join(stale, ", "))
		}
		if len(unread) > 0 {
			parts = append(parts, "not searched (no index): "+strings.Join(unread, ", "))
		}
		if len(absent) > 0 {
			parts = append(parts, "not searched (see above): "+strings.Join(absent, ", "))
		}
	}
	server := ""
	if h.Cfg != nil {
		server = h.Cfg.WorkspaceServer
	}
	if server != "" && len(keys) > 0 {
		lines, repos, scope, n, prod, ok := h.serverConsumers(ctx, server, keys, linked)
		ans.lines = append(ans.lines, lines...)
		ans.repos = append(ans.repos, repos...)
		ans.sites += n
		ans.production += prod
		parts = append(parts, scope)
		if !ok {
			ans.incomplete = true
		}
	} else if server != "" {
		parts = append(parts, "link server not asked (this kind of symbol has no cross-repository records)")
	} else {
		parts = append(parts, "repositories that are not linked are not covered")
	}
	ans.scope = strings.Join(parts, "; ")
	return ans
}

// linkedConsumers is linkedAnswer in the shape verify and test-plan read.
func (h *Handler) linkedConsumers(ctx context.Context, targets []linkTarget) (lines []string, scope string, sites int) {
	a := h.linkedAnswer(ctx, targets)
	return a.lines, a.scope, a.sites + a.matches
}

// rootConsumers answers for one linked repository.
func (h *Handler) rootConsumers(ctx context.Context, root workspace.Root, targets []linkTarget, keys []string) rootAnswer {
	rel := h.Workspace.DisplayPath(root)
	prefix := fmt.Sprintf("// repo %s  %s", root.ID, rel)
	if root.Cache {
		prefix += "  [cache clone, read-only]"
	}
	everyTargetKeyed := len(targets) > 0
	for _, t := range targets {
		if len(t.Keys) == 0 {
			everyTargetKeyed = false
		}
	}
	// A repository whose manifest does not name the changed package cannot
	// reference it: skip it without opening its index or asking git about
	// its tree. Only when every target has a key; a symbol with none is
	// looked for by text, which no manifest rules out.
	if everyTargetKeyed && !mayReference(root.Path, keys) {
		return rootAnswer{state: "skipped"}
	}
	stale, indexed := h.freshenLinked(ctx, root)
	res := rootAnswer{state: "resolved", stale: stale != ""}
	pin := requirePin(root.Path, keys)

	complete := map[string]bool{}
	if indexed {
		if st, err := grove.ReadIndexState(ctx, root.Path); err == nil {
			for _, a := range st.NativeComplete {
				complete[a] = true
			}
		}
	}
	// Which targets the index can answer with compiler evidence, and which
	// fall back to text.
	var resolvable []string
	var fallback []linkTarget
	why := ""
	for _, t := range targets {
		switch {
		case !indexed:
			fallback, why = append(fallback, t), "no index there"
		case len(t.Keys) == 0:
			fallback, why = append(fallback, t), kindReason(t)
		default:
			an := keyAnalyzer(t.Keys[0])
			if an != "" && !complete[an] {
				fallback, why = append(fallback, t), "the "+an+" compiler pass has not completed there"
			} else {
				resolvable = append(resolvable, t.Keys...)
			}
		}
	}

	leafOfKey := func(key string) string {
		name := key[strings.IndexByte(key, '#')+1:]
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	files := map[string][]string{}
	lineOf := func(file string, n int) (string, bool) {
		src, ok := files[file]
		if !ok {
			if data, err := os.ReadFile(filepath.Join(root.Path, filepath.FromSlash(file))); err == nil {
				src = strings.Split(string(data), "\n")
			}
			files[file] = src
		}
		if n < 1 || n > len(src) {
			return "", false
		}
		return strings.TrimSpace(strings.TrimRight(src[n-1], "\r")), true
	}
	shown := map[string]bool{} // file:line already printed as resolved
	var rows []string
	var sites []LinkedSite
	dropped := 0
	if len(resolvable) > 0 {
		refs, err := grove.ReadExternalRefs(ctx, root.Path, resolvable)
		if err != nil {
			return rootAnswer{state: "unread", lines: []string{prefix + "  NOT SEARCHED: its index could not be read (" + err.Error() + ")"}}
		}
		for _, ref := range refs {
			text, ok := lineOf(ref.File, ref.Line)
			// A row is a site only while the line on disk still names the
			// symbol: after an edit the index's coordinates point at
			// whatever now lives there.
			if !ok || !strings.Contains(text, leafOfKey(ref.Key)) {
				dropped++
				continue
			}
			at := fmt.Sprintf("%s/%s:%d", rel, ref.File, ref.Line)
			if ref.Col > 0 {
				at += fmt.Sprintf(":%d", ref.Col)
			}
			tag := ""
			if strings.HasPrefix(ref.Key, "py:") {
				// No compiler pass: the call names something the file
				// imported under this module and name.
				tag += "  [import-scoped]"
			}
			if isTestFilePath(ref.File) {
				tag += "  [test]"
			} else {
				res.production++
			}
			rows = append(rows, fmt.Sprintf("  %s: %s  [in %s]%s", at, text, ref.From, tag))
			ev := "resolved"
			if strings.HasPrefix(ref.Key, "py:") {
				ev = "import-scoped"
			}
			sites = append(sites, LinkedSite{File: ref.File, Line: ref.Line, Col: ref.Col, Text: text, From: ref.From, Evidence: ev, Test: isTestFilePath(ref.File)})
			shown[fmt.Sprintf("%s:%d", ref.File, ref.Line)] = true
			res.sites++
		}
	}
	// Go: the types here that implement an interface of the change set.
	// A key go:<path>#Iface names the interface, go:<path>#Iface.Method one
	// of its methods; a key that is no interface matches nothing.
	if complete["go"] {
		methodOf := map[string]map[string]bool{} // "path.Iface" -> methods asked for ("" = all)
		var ifaces []string
		for _, k := range resolvable {
			if !strings.HasPrefix(k, "go:") {
				continue
			}
			path, name, _ := strings.Cut(strings.TrimPrefix(k, "go:"), "#")
			typ, method, _ := strings.Cut(name, ".")
			id := path + "." + typ
			if methodOf[id] == nil {
				methodOf[id] = map[string]bool{}
				ifaces = append(ifaces, id)
			}
			methodOf[id][method] = true
		}
		impls, _ := grove.ReadExternalImplementers(ctx, root.Path, ifaces)
		for _, im := range impls {
			_, method, _ := strings.Cut(im.Method, ".")
			if !methodOf[im.Interface][""] && !methodOf[im.Interface][method] {
				continue
			}
			id := fmt.Sprintf("%s:%d", im.File, im.Line)
			if shown[id] {
				continue
			}
			shown[id] = true
			text, _ := lineOf(im.File, im.Line)
			tag := ""
			if isTestFilePath(im.File) {
				tag = "  [test]"
			} else {
				res.production++
			}
			short := im.Interface[strings.LastIndexByte(im.Interface, '/')+1:]
			rows = append(rows, fmt.Sprintf("  %s/%s:%d: %s  [implements %s]%s", rel, im.File, im.Line, text, short, tag))
			sites = append(sites, LinkedSite{File: im.File, Line: im.Line, Text: text, Evidence: "implements", Detail: short, Test: isTestFilePath(im.File)})
			res.sites++
		}
	}
	var matchRows []string
	more := 0
	for _, t := range fallback {
		hits, total := h.nameMatches(ctx, root, t)
		for _, hit := range hits {
			id := fmt.Sprintf("%s:%d", hit.File, hit.Line)
			if shown[id] {
				continue
			}
			shown[id] = true
			tag := "  [name match, not resolved]"
			if isTestFilePath(hit.File) {
				tag += "  [test]"
			}
			matchRows = append(matchRows, fmt.Sprintf("  %s/%s:%d: %s%s", rel, hit.File, hit.Line, strings.TrimSpace(hit.Text), tag))
			sites = append(sites, LinkedSite{File: hit.File, Line: hit.Line, Text: strings.TrimSpace(hit.Text), Evidence: "name-match", Test: isTestFilePath(hit.File)})
		}
		if total > len(hits) {
			more += total - len(hits)
		}
	}
	res.matches = len(matchRows)
	return finishRootAnswer(res, rootParts{root: root, rel: rel, prefix: prefix, stale: stale, pin: pin, why: why,
		fallback: len(fallback), resolvable: len(resolvable), dropped: dropped, more: more,
		rows: rows, matchRows: matchRows, sites: sites})
}

// rootParts is what rootConsumers found in one linked repository.
type rootParts struct {
	root                                workspace.Root
	rel, prefix, stale, pin, why        string
	fallback, resolvable, dropped, more int
	rows, matchRows                     []string
	sites                               []LinkedSite
}

// finishRootAnswer writes one linked repository's header, text lines and
// structured entry.
func finishRootAnswer(res rootAnswer, p rootParts) rootAnswer {
	header := p.prefix
	if p.stale != "" {
		header += "  " + p.stale
	}
	if p.pin != "" {
		header += "  " + p.pin
	}
	switch {
	case p.fallback > 0 && p.resolvable == 0:
		res.state, res.why = "name-matched", p.why
		header += fmt.Sprintf("  NAME-MATCHED ONLY (%s): whole-word text matches, not resolved by a compiler", p.why)
		if len(p.matchRows) == 0 {
			header += "; none found"
		}
	case p.fallback > 0:
		header += fmt.Sprintf("  (some symbols name-matched only: %s)", p.why)
	case len(p.rows) == 0:
		if p.pin == "" && p.stale == "" && p.dropped == 0 {
			return res // searched, nothing to say: it does not require the package
		}
		header += ": no references recorded"
	}
	if p.dropped > 0 {
		header += fmt.Sprintf("  (%d indexed row(s) dropped: the line on disk no longer names it)", p.dropped)
	}
	src := "checkout"
	if p.root.Cache {
		src = "clone"
	}
	state := res.state
	if state == "" {
		state = "resolved"
	}
	// The note is what the header says beyond the pin (stale, name-matched
	// only, no references recorded); the pin is its own field.
	note := strings.TrimSpace(strings.TrimPrefix(header, p.prefix))
	if p.pin != "" {
		note = strings.TrimSpace(strings.Replace(note, p.pin, "", 1))
	}
	sites := p.sites
	if sites == nil {
		sites = []LinkedSite{}
	}
	res.repo = &LinkedRepo{ID: p.root.ID, Path: p.rel, Source: src, Pin: strings.TrimPrefix(p.pin, "requires "), State: state, Note: note, Sites: sites}
	res.lines = append(res.lines, header)
	res.lines = append(res.lines, p.rows...)
	res.lines = append(res.lines, p.matchRows...)
	if p.more > 0 {
		res.lines = append(res.lines, fmt.Sprintf("  … %d more name match(es) not shown", p.more))
	}
	return res
}

func kindReason(t linkTarget) string {
	switch t.Kind {
	case "variable", "const", "field", "enum":
		return t.Kind + " references are not recorded across repositories"
	}
	if t.Language == "" {
		return "this kind of symbol has no cross-repository records"
	}
	return t.Language + " has no cross-repository records"
}

const nameMatchShown = 20

// nameMatches is the fallback tier: whole-word text matches of the symbol's
// name in files of its language family, kept only in files that also mention
// its owner type or its package. That is as precise as text gets, and every
// row built from it is labelled as unresolved.
func (h *Handler) nameMatches(ctx context.Context, root workspace.Root, t linkTarget) (hits []textsearch.Hit, total int) {
	if t.Name == "" {
		return nil, 0
	}
	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	opts := textsearch.Options{Regex: true, CaseSensitive: true, Glob: languageGlobs(t.Language), MaxHits: 400, MaxPerFile: 50}
	res := textsearch.Search(sctx, root.Path, `\b`+regexp.QuoteMeta(t.Name)+`\b`, opts)
	all := res.Hits

	// Narrow to files that mention the owner type (as a whole word) or
	// name the package the way an import does: the full Go import path, the
	// npm package name, the Java package, the top-level Python module. The
	// last path element alone ("lib") matches too much.
	type hint struct {
		pattern string
		regex   bool
	}
	var hints []hint
	if t.Owner != "" {
		hints = append(hints, hint{`\b` + regexp.QuoteMeta(t.Owner) + `\b`, true})
	}
	seenPkg := map[string]bool{}
	for _, k := range t.Keys {
		pkg := k[strings.IndexByte(k, ':')+1 : strings.IndexByte(k, '#')]
		if strings.HasPrefix(k, "py:") {
			pkg, _, _ = strings.Cut(pkg, ".")
		}
		if pkg != "" && !seenPkg[pkg] {
			seenPkg[pkg] = true
			hints = append(hints, hint{pkg, false})
		}
	}
	if len(hints) > 0 {
		mention := map[string]bool{}
		complete := true
		for _, hn := range hints {
			c := textsearch.Count(sctx, root.Path, hn.pattern, textsearch.Options{Regex: hn.regex, CaseSensitive: true, Glob: opts.Glob})
			if c.TimedOut {
				complete = false
			}
			for _, f := range c.Files {
				mention[f.File] = true
			}
		}
		if complete {
			kept := all[:0]
			for _, hit := range all {
				if mention[hit.File] {
					kept = append(kept, hit)
				}
			}
			all = kept
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		ti, tj := isTestFilePath(all[i].File), isTestFilePath(all[j].File)
		if ti != tj {
			return !ti // production code first
		}
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	total = len(all)
	if len(all) > nameMatchShown {
		all = all[:nameMatchShown]
	}
	return all, total
}

// mayReference reports whether a repository's root manifest names a package
// any of keys belongs to. It errs toward true: an ecosystem with no manifest
// test, a missing manifest, or a key it cannot place all mean "look".
func mayReference(root string, keys []string) bool {
	var gomod, pkgjson string
	read := func(name string, into *string) bool {
		if *into == "" {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				return false
			}
			*into = string(data)
		}
		return true
	}
	for _, k := range keys {
		eco, rest, _ := strings.Cut(k, ":")
		pkg, _, _ := strings.Cut(rest, "#")
		switch eco {
		case "go":
			if !read("go.mod", &gomod) {
				return true
			}
			// The package is a module go.mod names (require or replace), or
			// a package inside one. Module paths have any number of segments:
			// a prefix of at least three missed go.uber.org/zap/zapcore,
			// k8s.io/client-go/kubernetes and example.com/lib/store, so their
			// consumers were skipped as not requiring the package.
			for _, mod := range goModPaths(gomod) {
				if pkg == mod || strings.HasPrefix(pkg, mod+"/") {
					return true
				}
			}
		case "npm":
			if !read("package.json", &pkgjson) {
				return true
			}
			if strings.Contains(pkgjson, `"`+pkg+`"`) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// goModPaths lists the module paths a go.mod names: its module, requires
// and both sides of replaces (local directories excluded).
func goModPaths(gomod string) []string {
	var out []string
	for _, line := range strings.Split(gomod, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.Fields(line) {
			f = strings.Trim(f, "()")
			switch {
			case f == "" || f == "=>" || f == "module" || f == "require" || f == "replace" || f == "exclude" || f == "retract":
			case strings.HasPrefix(f, ".") || strings.HasPrefix(f, "/"):
			case strings.HasPrefix(f, "v") && len(f) > 1 && f[1] >= '0' && f[1] <= '9':
			case strings.Contains(f, ".") || strings.Contains(f, "/"):
				out = append(out, f)
			}
		}
	}
	return out
}

// serverConsumers asks the shared link server for references in
// repositories that are not on this machine. Each answer describes a
// repository at the commit it uploaded, which the lines say; a server that
// cannot be reached is reported as not covered, never as no consumers.
func (h *Handler) serverConsumers(ctx context.Context, server string, keys []string, linked []workspace.Root) (lines []string, repos []LinkedRepo, scope string, sites, production int, ok bool) {
	req := linkserver.ConsumersRequest{Keys: keys}
	for _, pkg := range grove.ManifestPackages(h.Root) {
		req.Packages = append(req.Packages, pkg.Ecosystem+":"+pkg.Name)
	}
	for _, root := range append([]string{h.Root}, rootPaths(linked)...) {
		for _, pkg := range grove.ManifestPackages(root) {
			req.ExcludePackages = append(req.ExcludePackages, pkg.Ecosystem+":"+pkg.Name)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := linkserver.AskConsumers(rctx, server, req)
	if err != nil {
		return nil, nil, fmt.Sprintf("server %s NOT REACHED (%v): repositories only it knows are not covered", server, err), 0, 0, false
	}
	for _, repo := range resp.Repos {
		sha := repo.SHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		header := fmt.Sprintf("// repo %s@%s", repo.Repo, sha)
		if repo.Ref != "" {
			header += " (" + repo.Ref + ")"
		}
		header += "  from the server, not checked out"
		if repo.Requires != "" {
			header += "  requires " + repo.Requires
		}
		if day, _, ok := strings.Cut(repo.Created, "T"); ok {
			header += "  uploaded " + day
		}
		lines = append(lines, header)
		lr := LinkedRepo{ID: repo.Repo, Source: "server", Commit: repo.SHA, Ref: repo.Ref, Pin: repo.Requires, State: "resolved", Sites: []LinkedSite{}}
		if day, _, ok := strings.Cut(repo.Created, "T"); ok {
			lr.Note = "uploaded " + day
		}
		for _, site := range repo.Sites {
			ev := "resolved"
			if strings.HasPrefix(site.Key, "py:") {
				ev = "import-scoped"
			}
			lr.Sites = append(lr.Sites, LinkedSite{File: site.File, Line: site.Line, Col: site.Col, Text: site.Text, From: site.From, Evidence: ev, Test: site.Test})
			at := fmt.Sprintf("%s:%s:%d", repo.Repo, site.File, site.Line)
			if site.Col > 0 {
				at += fmt.Sprintf(":%d", site.Col)
			}
			tag := ""
			if strings.HasPrefix(site.Key, "py:") {
				// As for a linked checkout: no compiler pass, the call
				// names something the file imported under this module.
				tag += "  [import-scoped]"
			}
			if site.Test {
				tag += "  [test]"
			} else {
				production++
			}
			lines = append(lines, fmt.Sprintf("  %s: %s  [in %s]%s", at, site.Text, site.From, tag))
			sites++
		}
		repos = append(repos, lr)
	}
	scope = fmt.Sprintf("server %s: %d site(s) in %d of %d uploaded repositories, each at its uploaded commit; "+
		"repositories neither linked nor uploaded are not covered", server, sites, len(resp.Repos), resp.Searched)
	if resp.Unreadable > 0 {
		// The token may not read every uploaded repository: those were
		// not searched, so the answer cannot claim completeness.
		scope += fmt.Sprintf("; %d uploaded repositories NOT READABLE with this token, not searched", resp.Unreadable)
		return lines, repos, scope, sites, production, false
	}
	return lines, repos, scope, sites, production, true
}

func rootPaths(roots []workspace.Root) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, r.Path)
	}
	return out
}

// requirePin says which version of the changed package a repository's
// manifests require, when they require it: the checkout searched may be
// ahead of or behind that pin.
func requirePin(root string, keys []string) string {
	for _, req := range grove.ManifestRequires(root) {
		if req.CoMember {
			continue
		}
		for _, k := range keys {
			eco, rest, _ := strings.Cut(k, ":")
			pkg, _, _ := strings.Cut(rest, "#")
			if eco != req.Ecosystem || !(pkg == req.Name || (eco == "go" && strings.HasPrefix(pkg, req.Name+"/"))) {
				continue
			}
			pin := "requires " + req.Name
			if req.Version != "" {
				pin += " " + req.Version
			}
			if req.LocalPath != "" {
				// go.work, a replace directive, file: — the version is
				// not what it is built against.
				pin += ", built against the local checkout"
			}
			return pin
		}
	}
	return ""
}
