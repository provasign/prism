package graphui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

func sym(file, name, kind string, line int) grove.SymbolRecord {
	return grove.SymbolRecord{
		ID: file + "::" + name + "@sha1", FilePath: file, Kind: kind, Name: name,
		QualifiedName: name, Language: "go", Span: grove.SpanInfo{Start: line, End: line + 2},
		Signature: "sig " + name, RawText: "body of " + name, Exports: true,
	}
}

func edge(from, to grove.SymbolRecord, kind string) grove.Edge {
	return grove.Edge{From: from.ID, To: to.ID, Type: kind, Source: "native"}
}

// fixture: app/api (handler.go, handler_test.go) -> app/store (db.go) ->
// lib/deep/only/util (util.go); a README document symbol that must not
// appear; a struct with a field and a method; a function with a local type.
func fixture() *Model {
	serve := sym("app/api/handler.go", "Serve", "function", 10)
	route := sym("app/api/handler.go", "route", "function", 30)
	tserve := sym("app/api/handler_test.go", "TestServe", "function", 5)
	db := sym("app/store/db.go", "DB", "struct", 3)
	conn := sym("app/store/db.go", "DB.conn", "field", 4)
	get := sym("app/store/db.go", "DB.Get", "method", 8)
	open := sym("app/store/db.go", "Open", "function", 20)
	local := sym("app/store/db.go", "Open.opts", "struct", 21)
	util := sym("lib/deep/only/util/util.go", "Join", "function", 1)
	mainFn := sym("main.go", "main", "function", 1)
	doc := sym("README.md", "Intro", "document", 1)
	return BuildModel("/repo/demo",
		[]grove.SymbolRecord{serve, route, tserve, db, conn, get, open, local, util, mainFn, doc},
		[]grove.Edge{
			edge(serve, route, "calls"),
			edge(serve, get, "calls"),
			edge(serve, db, "uses-type"),
			edge(route, open, "calls"),
			edge(tserve, serve, "calls"),
			edge(get, util, "calls"),
			edge(open, local, "uses-type"),
			edge(mainFn, serve, "calls"),
			edge(db, conn, "contains"),
			edge(db, get, "contains"),
			edge(open, local, "contains"),
			{From: "app/api/handler.go", To: "fmt", Type: "imports"}, // unresolved: dropped
			edge(tserve, serve, "tests"),                             // coverage, not dependency
		})
}

func nodeIDs(g *Graph) map[string]Node {
	out := map[string]Node{}
	for _, n := range g.Nodes {
		out[n.ID] = n
	}
	return out
}

func linkOf(g *Graph, from, to string) *Link {
	for i := range g.Links {
		if g.Links[i].From == from && g.Links[i].To == to {
			return &g.Links[i]
		}
	}
	return nil
}

func mustGraph(t *testing.T, m *Model, scope string, o Options) *Graph {
	t.Helper()
	g, err := m.Graph(scope, o)
	if err != nil {
		t.Fatalf("Graph(%q): %v", scope, err)
	}
	return g
}

func TestRootTreeCollapsesSingleChildChains(t *testing.T) {
	g := mustGraph(t, fixture(), "", Options{})
	ns := nodeIDs(g)
	for _, want := range []string{"d:app", "d:lib/deep/only/util", "f:main.go"} {
		if _, ok := ns[want]; !ok {
			t.Errorf("root view is missing %s; have %v", want, keys(ns))
		}
	}
	if len(ns) != 3 {
		t.Errorf("root view has %d nodes, want 3: %v", len(ns), keys(ns))
	}
	if l := linkOf(g, "d:app", "d:lib/deep/only/util"); l == nil || l.Weight != 1 || l.Kinds["calls"] != 1 {
		t.Errorf("app -> util link = %+v, want one call", l)
	}
	if l := linkOf(g, "f:main.go", "d:app"); l == nil || len(l.Sites) != 1 || l.Sites[0].FromSymbol != "main" || l.Sites[0].ToSymbol != "Serve" {
		t.Errorf("main.go -> app link = %+v, want the main->Serve site", l)
	}
	if g.TestFilesExcluded != 1 {
		t.Errorf("TestFilesExcluded = %d, want 1", g.TestFilesExcluded)
	}
	if g.Scope.Files != 4 || g.Scope.Symbols != 9 {
		t.Errorf("scope counts = %d files / %d symbols, want 4 / 9", g.Scope.Files, g.Scope.Symbols)
	}
}

func TestRootFlatListsEveryPackage(t *testing.T) {
	ns := nodeIDs(mustGraph(t, fixture(), "", Options{Flat: true}))
	for _, want := range []string{"d:app/api", "d:app/store", "d:lib/deep/only/util", "f:main.go"} {
		if _, ok := ns[want]; !ok {
			t.Errorf("flat root is missing %s; have %v", want, keys(ns))
		}
	}
}

func TestDirectoryViewShowsFilesAndOutsideContext(t *testing.T) {
	g := mustGraph(t, fixture(), "d:app/store", Options{})
	ns := nodeIDs(g)
	if n, ok := ns["f:app/store/db.go"]; !ok || n.Outside {
		t.Fatalf("db.go should be an inside file node: %+v", n)
	}
	// app/api branches off under the shared ancestor "app"; util under "".
	for id, wantLabel := range map[string]string{"d:app/api": "app/api", "d:lib": "lib", "f:main.go": ""} {
		n, ok := ns[id]
		if id == "f:main.go" {
			if ok {
				t.Errorf("main.go has no link into app/store and must not be drawn")
			}
			continue
		}
		if !ok || !n.Outside || n.Label != wantLabel {
			t.Errorf("outside node %s = %+v (present %v), want outside with label %q", id, n, ok, wantLabel)
		}
	}
	if l := linkOf(g, "d:app/api", "f:app/store/db.go"); l == nil || l.Weight != 3 {
		t.Errorf("api -> db.go = %+v, want weight 3 (2 calls + 1 uses-type)", l)
	}
	if got := len(g.Crumbs); got != 3 || g.Crumbs[2].ID != "d:app/store" {
		t.Errorf("crumbs = %+v", g.Crumbs)
	}
}

func TestTestsAreExcludedUnlessAskedOrInScope(t *testing.T) {
	m := fixture()
	if _, ok := nodeIDs(mustGraph(t, m, "d:app/api", Options{}))["f:app/api/handler_test.go"]; ok {
		t.Error("test file drawn with tests off")
	}
	if _, ok := nodeIDs(mustGraph(t, m, "d:app/api", Options{IncludeTests: true}))["f:app/api/handler_test.go"]; !ok {
		t.Error("test file missing with tests on")
	}
	g := mustGraph(t, m, "f:app/api/handler_test.go", Options{})
	if !g.IncludeTests || len(g.Nodes) == 0 {
		t.Errorf("a test file scope must show its own symbols: includeTests=%v nodes=%d", g.IncludeTests, len(g.Nodes))
	}
}

func TestFileViewRollsFieldsAndLocalsIntoTheirContainer(t *testing.T) {
	g := mustGraph(t, fixture(), "f:app/store/db.go", Options{})
	ns := nodeIDs(g)
	for _, want := range []string{"s:app/store/db.go::DB@sha1", "s:app/store/db.go::DB.Get@sha1", "s:app/store/db.go::Open@sha1"} {
		if _, ok := ns[want]; !ok {
			t.Errorf("file view is missing %s; have %v", want, keys(ns))
		}
	}
	for _, gone := range []string{"s:app/store/db.go::DB.conn@sha1", "s:app/store/db.go::Open.opts@sha1"} {
		if _, ok := ns[gone]; ok {
			t.Errorf("%s should be rolled into its container", gone)
		}
	}
	if n := ns["f:app/api/handler.go"]; !n.Outside {
		t.Errorf("handler.go should be outside context: %+v", n)
	}
	if l := linkOf(g, "f:app/api/handler.go", "s:app/store/db.go::Open@sha1"); l == nil || l.Weight != 1 {
		t.Errorf("handler.go -> Open = %+v", l)
	}
	// Open uses its own local type: a self-link once rolled up, not drawn.
	if l := linkOf(g, "s:app/store/db.go::Open@sha1", "s:app/store/db.go::Open@sha1"); l != nil {
		t.Errorf("self link drawn: %+v", l)
	}
}

func TestSymbolViewShowsCallersCalleesAndMembers(t *testing.T) {
	m := fixture()
	g := mustGraph(t, m, "s:app/api/handler.go::Serve@sha1", Options{})
	ns := nodeIDs(g)
	focus := ns["s:app/api/handler.go::Serve@sha1"]
	if !focus.Focus || focus.In != 1 || focus.Out != 3 {
		t.Errorf("focus = %+v, want focus with 1 in (main; the test is excluded) and 3 out", focus)
	}
	if _, ok := ns["s:app/api/handler_test.go::TestServe@sha1"]; ok {
		t.Error("test caller drawn with tests off")
	}
	g = mustGraph(t, m, "s:app/store/db.go::DB@sha1", Options{})
	ns = nodeIDs(g)
	for _, member := range []string{"s:app/store/db.go::DB.conn@sha1", "s:app/store/db.go::DB.Get@sha1"} {
		if n, ok := ns[member]; !ok || !n.Member {
			t.Errorf("member %s = %+v (present %v)", member, n, ok)
		}
	}
	if l := linkOf(g, "s:app/store/db.go::DB@sha1", "s:app/store/db.go::DB.Get@sha1"); l == nil || l.Kinds["contains"] != 1 {
		t.Errorf("contains link = %+v", l)
	}
	if got := g.Crumbs[len(g.Crumbs)-1].Label; got != "DB" {
		t.Errorf("last crumb = %q, want DB", got)
	}
}

func TestSymbolIDSurvivesAReindex(t *testing.T) {
	// The blob suffix changes when the file changes; a bookmarked URL
	// should still resolve.
	g := mustGraph(t, fixture(), "s:app/api/handler.go::Serve@othersha", Options{})
	if g.Scope.Label != "Serve" {
		t.Errorf("scope = %+v", g.Scope)
	}
}

func TestUnknownScopesAreErrors(t *testing.T) {
	m := fixture()
	for _, scope := range []string{"d:nope", "f:nope.go", "s:nope", "x:what", "f:../../etc/passwd"} {
		if _, err := m.Graph(scope, Options{}); err == nil {
			t.Errorf("Graph(%q) succeeded, want an error", scope)
		}
	}
}

func TestInsideNodeCapIsReported(t *testing.T) {
	var syms []grove.SymbolRecord
	for i := 0; i < maxInsideNodes+25; i++ {
		syms = append(syms, sym("pkg/f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26)+".go", "F", "function", 1))
	}
	g := mustGraph(t, BuildModel("/r", syms, nil), "d:pkg", Options{})
	if len(g.Nodes) != maxInsideNodes || g.Hidden != 25 {
		t.Errorf("nodes=%d hidden=%d, want %d and 25", len(g.Nodes), g.Hidden, maxInsideNodes)
	}
}

func TestSearchRanksExactThenPrefixAndSkipsDocuments(t *testing.T) {
	m := fixture()
	hits := m.Search("serve", 10)
	if len(hits) < 2 || hits[0].Label != "Serve" || hits[len(hits)-1].Label != "TestServe" {
		t.Errorf("hits = %+v, want Serve first and the test last", hits)
	}
	if hits := m.Search("intro", 10); len(hits) != 0 {
		t.Errorf("document symbol found: %+v", hits)
	}
	var types []string
	for _, h := range m.Search("store", 10) {
		types = append(types, h.Type)
	}
	if !strings.Contains(strings.Join(types, ","), "dir") {
		t.Errorf("directory search found %v, want a dir hit", types)
	}
	if hits := m.Search("db.go", 10); len(hits) == 0 || hits[0].ID != "f:app/store/db.go" {
		t.Errorf("file search = %+v", hits)
	}
	if m.Search("  ", 10) != nil {
		t.Error("blank query returned hits")
	}
}

func TestSourceAndMeta(t *testing.T) {
	m := fixture()
	src, err := m.Source("s:app/api/handler.go::Serve@sha1")
	if err != nil || src.Text != "body of Serve" || src.Start != 10 {
		t.Errorf("Source = %+v, %v", src, err)
	}
	if _, err := m.Source("f:app/api/handler.go"); err == nil {
		t.Error("Source of a file succeeded")
	}
	meta := m.Meta()
	if meta.Name != "demo" || meta.Files != 5 || meta.Symbols != 10 || meta.Edges != 8 || meta.TestFiles != 1 || meta.Dirs != 3 {
		t.Errorf("Meta = %+v", meta)
	}
}

func TestGraphIsDeterministic(t *testing.T) {
	a, _ := json.Marshal(mustGraph(t, fixture(), "", Options{IncludeTests: true, Flat: true}))
	for i := 0; i < 5; i++ {
		b, _ := json.Marshal(mustGraph(t, fixture(), "", Options{IncludeTests: true, Flat: true}))
		if string(a) != string(b) {
			t.Fatal("two builds of the same snapshot rendered different graphs")
		}
	}
}

func testServer(t *testing.T, reindexed *int) *httptest.Server {
	t.Helper()
	snap := func(context.Context) ([]grove.SymbolRecord, []grove.Edge, error) {
		a, b := sym("a/a.go", "A", "function", 1), sym("b/b.go", "B", "function", 1)
		return []grove.SymbolRecord{a, b}, []grove.Edge{edge(a, b, "calls")}, nil
	}
	srv, err := NewServer(context.Background(), "/repo/demo", "test", snap, func(context.Context) error { *reindexed++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestServerEndpoints(t *testing.T) {
	reindexed := 0
	ts := testServer(t, &reindexed)
	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, b.String()
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, "<title>Prism graph</title>") {
		t.Errorf("page: %d", code)
	}
	if code, body := get("/api/meta"); code != 200 || !strings.Contains(body, `"name":"demo"`) {
		t.Errorf("meta: %d %s", code, body)
	}
	if code, body := get("/api/graph?scope="); code != 200 || !strings.Contains(body, `"from":"d:a","to":"d:b"`) {
		t.Errorf("graph: %d %s", code, body)
	}
	if code, body := get("/api/graph?scope=d:missing"); code != 404 || !strings.Contains(body, "not in the index") {
		t.Errorf("missing scope: %d %s", code, body)
	}
	if code, body := get("/api/search?q=zzz"); code != 200 || !strings.Contains(body, `"hits":[]`) {
		t.Errorf("empty search: %d %s", code, body)
	}
	if code, _ := get("/api/source?id=" + "s:a/a.go::A@sha1"); code != 200 {
		t.Errorf("source: %d", code)
	}
	if code, _ := get("/nope"); code != 404 {
		t.Errorf("unknown path: %d", code)
	}

	// Reload changes state: refused without the custom header.
	resp, err := http.Post(ts.URL+"/api/reload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || reindexed != 0 {
		t.Errorf("reload without header: %d, reindexed=%d", resp.StatusCode, reindexed)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/reload", nil)
	req.Header.Set("X-Prism-Graph", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || reindexed != 1 {
		t.Errorf("reload: %d, reindexed=%d", resp.StatusCode, reindexed)
	}
}

func TestServerRefusesNonLoopbackHost(t *testing.T) {
	n := 0
	ts := testServer(t, &n)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/meta", nil)
	req.Host = "attacker.example:8787" // DNS rebinding: their name, our address
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host got %d, want 403", resp.StatusCode)
	}
	for host, want := range map[string]bool{"127.0.0.1:8787": true, "localhost:1": true, "[::1]:9": true, "localhost": true,
		"example.com": false, "127.0.0.1.evil.com:80": false, "": false} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func keys(m map[string]Node) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func rec(file, qname, kind string, line int) map[string]any {
	return map[string]any{"filePath": file, "qualifiedName": qname, "name": qname, "kind": kind, "line": float64(line)}
}

func list(v ...map[string]any) []any {
	out := make([]any, len(v))
	for i := range v {
		out[i] = v[i]
	}
	return out
}

func roles(im *Impact) map[string]string {
	out := map[string]string{}
	for _, n := range im.Nodes {
		out[n.Label] = n.Role
	}
	return out
}

func TestImpactMapsTheAnswerOntoTheGraph(t *testing.T) {
	m := fixture()
	caller := rec("app/api/handler.go", "Serve", "function", 10)
	caller["evidence"] = []any{map[string]any{"line": float64(12), "text": "db.Get()"}}
	raw := map[string]any{
		"query": "DB.Get", "completeness": "closed", "safeToClaimComplete": true, "totalSites": float64(3),
		"declarations": list(rec("app/store/db.go", "DB.Get", "method", 8)),
		"family":       list(rec("gen/mock.go", "MockDB.Get", "method", 4)), // not indexed: kept, unmapped
		"callers":      list(caller, rec("app/api/handler_test.go", "TestServe", "function", 5)),
		"relaySites":   []any{"app/api/handler.go:10:Serve [function]"},
	}
	im, err := m.Impact("s:app/store/db.go::DB.Get@sha1", raw, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	r := roles(im)
	if r["DB.Get"] != RoleTarget || r["Serve"] != RoleCaller || r["MockDB.Get"] != RoleFamily {
		t.Errorf("roles = %v", r)
	}
	if _, ok := r["TestServe"]; ok || im.TestsHidden != 1 {
		t.Errorf("test caller drawn with tests off: %v, hidden=%d", r, im.TestsHidden)
	}
	if !im.SafeToClaimComplete || im.Completeness != "closed" || len(im.RelaySites) != 1 {
		t.Errorf("verdict not carried: %+v", im)
	}
	var site *ImpactSite
	for i := range im.Sites {
		if im.Sites[i].Symbol == "Serve" {
			site = &im.Sites[i]
		}
	}
	if site == nil || site.Line != 12 || site.Text != "db.Get()" || site.NodeID != "s:app/api/handler.go::Serve@sha1" {
		t.Errorf("caller site = %+v", site)
	}
	if im.Sites[0].Role != RoleTarget || im.Sites[0].Access != "decl" {
		t.Errorf("first site should be the declaration: %+v", im.Sites[0])
	}
	if linkOf(&Graph{Links: im.Links}, "s:app/api/handler.go::Serve@sha1", "s:app/store/db.go::DB.Get@sha1") == nil {
		t.Error("caller -> target link missing")
	}
	if linkOf(&Graph{Links: im.Links}, "x:gen/mock.go:4", "s:app/store/db.go::DB.Get@sha1") == nil {
		t.Error("unindexed family member should be tied to the target")
	}

	im2, _ := m.Impact("s:app/store/db.go::DB.Get@sha1", raw, 2, false)
	if r := roles(im2); r["main"] != RoleTransitive {
		t.Errorf("depth 2 should reach main through Serve: %v", r)
	}
	im3, _ := m.Impact("s:app/store/db.go::DB.Get@sha1", raw, 2, true)
	if r := roles(im3); r["TestServe"] != RoleCaller {
		t.Errorf("tests on: TestServe = %q", r["TestServe"])
	}
}

func TestImpactMemberAccessesAttachToTheirEnclosingSymbol(t *testing.T) {
	m := fixture()
	raw := map[string]any{
		"declarations": list(rec("app/store/db.go", "DB.conn", "field", 4)),
		"memberKind":   "field",
		"accesses": []any{
			map[string]any{"filePath": "app/store/db.go", "line": float64(4), "enclosing": "DB.conn", "access": "decl"},
			map[string]any{"filePath": "app/store/db.go", "line": float64(9), "enclosing": "DB.Get", "access": "read", "text": "d.conn"},
		},
	}
	im, err := m.Impact("s:app/store/db.go::DB.conn@sha1", raw, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if r := roles(im); r["DB.Get"] != RoleAccess {
		t.Errorf("roles = %v", r)
	}
	if len(im.Sites) != 2 || im.Sites[1].Access != "read" || im.Sites[1].NodeID != "s:app/store/db.go::DB.Get@sha1" {
		t.Errorf("sites = %+v", im.Sites)
	}
}

func TestLargeImpactIsGroupedByFile(t *testing.T) {
	target := sym("lib/core.go", "Do", "function", 1)
	syms := []grove.SymbolRecord{target}
	var callers []map[string]any
	for i := 0; i < maxSiteNodes+10; i++ {
		f := fmt.Sprintf("pkg/f%d.go", i%7)
		s := sym(f, fmt.Sprintf("C%d", i), "function", 10+i)
		syms = append(syms, s)
		callers = append(callers, rec(f, s.Name, "function", s.Span.Start))
	}
	m := BuildModel("/r", syms, nil)
	im, err := m.Impact("s:"+target.ID, map[string]any{"callers": list(callers...)}, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !im.GroupedByFile || len(im.Nodes) != 8 {
		t.Fatalf("grouped=%v nodes=%d, want 7 file groups + target", im.GroupedByFile, len(im.Nodes))
	}
	total := 0
	for _, n := range im.Nodes {
		if strings.HasPrefix(n.ID, "g:site:") {
			total += n.Symbols
		}
	}
	if total != maxSiteNodes+10 || im.Counts[RoleCaller] != maxSiteNodes+10 {
		t.Errorf("group sizes sum to %d, counts=%v", total, im.Counts)
	}
	for _, s := range im.Sites {
		if s.Role == RoleCaller && !strings.HasPrefix(s.NodeID, "g:site:pkg/") {
			t.Fatalf("site not remapped to its file group: %+v", s)
		}
	}
}

func TestImpactEndpoint(t *testing.T) {
	calls := 0
	impact := func(_ context.Context, query, file string) (map[string]any, error) {
		calls++
		switch {
		case query == "A" && file == "a/a.go" && calls == 1:
			return map[string]any{"status": "needs_file_scope", "alternatives": []any{map[string]any{"filePath": "a/a.go"}}}, nil
		case query == "A":
			return map[string]any{"query": "A", "declarations": list(rec("a/a.go", "A", "function", 1)), "completeness": "closed"}, nil
		}
		return nil, fmt.Errorf("change-impact: no method named %q", query)
	}
	snap := func(context.Context) ([]grove.SymbolRecord, []grove.Edge, error) {
		a, b := sym("a/a.go", "A", "function", 1), sym("b/b.go", "B", "function", 1)
		return []grove.SymbolRecord{a, b}, []grove.Edge{edge(b, a, "calls")}, nil
	}
	srv, err := NewServer(context.Background(), "/repo/demo", "test", snap, nil, impact)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	get := func(id string) (int, map[string]any) {
		resp, err := http.Get(ts.URL + "/api/impact?depth=2&id=" + id)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	code, body := get("s:a/a.go::A@sha1")
	if code != 200 || body["completeness"] != "closed" {
		t.Fatalf("impact: %d %v", code, body)
	}
	if code, _ := get("s:a/a.go::A@sha1"); code != 200 || calls != 2 {
		t.Errorf("second request should be cached: calls=%d", calls)
	}
	if code, body := get("s:b/b.go::B@sha1"); code != 422 || !strings.Contains(fmt.Sprint(body["error"]), "no method named") {
		t.Errorf("tool error: %d %v", code, body)
	}
	if code, _ := get("d:a"); code != 404 {
		t.Errorf("non-symbol id: %d", code)
	}
}
