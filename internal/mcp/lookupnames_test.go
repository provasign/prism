package mcp

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/config"
	"github.com/provasign/prism/internal/grove"
)

// namesRepo indexes files under a root directory called rootName (the
// repository's own name is a valid leading qualifier: "gin.Default").
// fixture, when set, is a directory under testdata/lookupnames copied first.
func namesRepo(t *testing.T, rootName, fixture string, files map[string]string) *Handler {
	t.Helper()
	dir := filepath.Join(t.TempDir(), rootName)
	if fixture != "" {
		src := filepath.Join("testdata", "lookupnames", fixture)
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if files == nil {
				files = map[string]string{}
			}
			files[filepath.ToSlash(rel)] = string(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gc := grove.NewClient("", "").WithTokenFromDir(dir)
	if err := gc.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gc.Shutdown() })
	h := NewHandler(config.Default(), dir, gc)
	if _, err := h.Invoke("prism_index", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return h
}

// nameCase is one lookup: want "QN@file-suffix" must resolve confidently to
// that symbol; want "" must NOT produce a confident answer (NO EXACT MATCH,
// nothing, or an ambiguity list are all fine — a single delivered body is not).
type nameCase struct {
	q, want string
}

func runNameCases(t *testing.T, h *Handler, cases []nameCase) {
	t.Helper()
	for _, c := range cases {
		out, err := h.Invoke("prism_lookup", map[string]any{"name": c.q})
		if err != nil {
			t.Errorf("%s: %v", c.q, err)
			continue
		}
		m := out.(map[string]any)
		sym, _ := m["symbol"].(grove.SymbolRecord)
		confident := sym.ID != "" && m["matched"] != false && m["ambiguous"] != true
		got := lookupSymbolName(sym) + "@" + sym.FilePath
		if c.want == "" {
			if confident {
				t.Errorf("%-45q confident WRONG answer %s (note=%v)", c.q, got, m["note"])
			}
			continue
		}
		qn, file, _ := strings.Cut(c.want, "@")
		if !confident || lookupSymbolName(sym) != qn || !strings.HasSuffix(sym.FilePath, file) {
			t.Errorf("%-45q want %s, got %s matched=%v ambiguous=%v cands=%v note=%v",
				c.q, c.want, got, m["matched"], m["ambiguous"], m["candidates"], m["note"])
		}
	}
}

func ginNamesRepo(t *testing.T) *Handler {
	return namesRepo(t, "gin", "", map[string]string{
		"go.mod":             "module github.com/gin-gonic/gin\n\ngo 1.21\n",
		"gin.go":             "package gin\n\ntype Engine struct {\n\tRouterGroup\n\tpool int\n}\n\nfunc Default() *Engine { return &Engine{} }\n",
		"routergroup.go":     "package gin\n\ntype IRoutes interface {\n\tGET(string) IRoutes\n}\n\ntype RouterGroup struct {\n\tbasePath string\n}\n\nfunc (group *RouterGroup) GET(relativePath string) IRoutes { return nil }\n",
		"context.go":         "package gin\n\ntype Context struct {\n\tErrors []string\n}\n\nfunc (c *Context) JSON(code int, obj any) {}\n\nfunc (c *Context) String(code int) {}\n",
		"recovery.go":        "package gin\n\ntype HandlerFunc func(*Context)\n\nfunc Recovery() HandlerFunc { return nil }\n",
		"tree.go":            "package gin\n\ntype Param struct{ Key string }\n\ntype Params []Param\n\nfunc (ps Params) Get(name string) (string, bool) { return \"\", false }\n",
		"render/json.go":     "package render\n\ntype JSON struct {\n\tData any\n}\n\nfunc (r JSON) Render() error { return nil }\n",
		"binding/binding.go": "package binding\n\nfunc Default(method string) string { return method }\n",
		"ginS/gins.go":       "package ginS\n\nfunc GET(relativePath string) {}\n",
	})
}

// TestLookupNames_QualifierNeverSelectsUnrelatedSymbol replays the 32
// confident-wrong rows of the 2026-09-26 name sweep on small fixtures. Before
// the fix a qualifier never rejected an owner-less symbol (free functions,
// top-level types) nor any C++ symbol, so "Context.Recovery" delivered the
// free function Recovery as an exact match.
func TestLookupNames_QualifierNeverSelectsUnrelatedSymbol(t *testing.T) {
	t.Run("go", func(t *testing.T) {
		runNameCases(t, ginNamesRepo(t), []nameCase{
			{"(*Context).JSON", "Context.JSON@context.go"},
			{"gin.(*Context).JSON", "Context.JSON@context.go"},
			{"Context.Recovery", ""},
			{"render.Recovery", ""},
			{"binding.Recovery", ""},
			{"Engine.JSON", ""},
			{"Params.JSON", ""},
			{"render.Context", ""},
			{"IRoutes.GET", ""},
			{"Engine.GET", "RouterGroup.GET@routergroup.go"},
			{"render.Context.JSON", ""},
			{"Engine.Default", ""},
		})
	})
	t.Run("java", func(t *testing.T) {
		runNameCases(t, javaNamesRepo(t), []nameCase{
			{"com.fasterxml.jackson.databind.ser.ObjectMapper", ""},
			{"com.example.ObjectMapper.readValue", ""},
			{"JsonNode.readValue", ""},
		})
	})
	t.Run("typescript", func(t *testing.T) {
		runNameCases(t, tsNamesRepo(t), []nameCase{
			{"Repository.getRepository", ""},
			{"Repository.useContainer", ""},
			{"DataSource.getFromContainer", ""},
		})
	})
	t.Run("javascript", func(t *testing.T) {
		runNameCases(t, jsNamesRepo(t), []nameCase{
			{"response.json", "res.json@lib/response.js"},
			{"lib/response.json", "res.json@lib/response.js"},
			{"express.response.json", "res.json@lib/response.js"},
			{"View.prototype.render", "View.render@lib/view.js"},
			{"app.prototype.render", "app.render@lib/application.js"},
			{"Res.json", "res.json@lib/response.js"},
			{"req.json", ""},
			{"res.createApplication", ""},
			{"app.logerror", ""},
		})
	})
	t.Run("python", func(t *testing.T) {
		runNameCases(t, pyNamesRepo(t), []nameCase{
			{"flask.app.jsonify", ""},
			{"Flask.jsonify", ""},
		})
	})
	t.Run("c", func(t *testing.T) {
		runNameCases(t, cNamesRepo(t), []nameCase{
			{"json_t.key", ""},
			{"hashtable_pair.json_object_get", ""},
		})
	})
	t.Run("cpp", func(t *testing.T) {
		runNameCases(t, namesRepo(t, "cppfx", "cppfx", nil), []nameCase{
			{"Rect.radius", ""},
			{"Rect::radius", ""},
			{"other::Circle::area", ""},
		})
	})
	t.Run("rust-swift-kotlin", func(t *testing.T) {
		runNameCases(t, namesRepo(t, "rustfx", "rustfx", nil), []nameCase{{"Config.parse_config", ""}, {"Config::connect", ""}})
		runNameCases(t, namesRepo(t, "swiftfx", "swiftfx", nil), []nameCase{{"User.makeSession", ""}})
		runNameCases(t, namesRepo(t, "ktfx", "ktfx", nil), []nameCase{{"Item.topLevelHelper", ""}})
	})
}

func javaNamesRepo(t *testing.T) *Handler {
	const base = "src/main/java/com/fasterxml/jackson/databind/"
	return namesRepo(t, "jackson-databind", "", map[string]string{
		base + "ObjectMapper.java": "package com.fasterxml.jackson.databind;\n\npublic class ObjectMapper {\n" +
			"    public ObjectMapper() {}\n" +
			"    public <T> T readValue(String content, Class<T> valueType) { return null; }\n" +
			"    public String writeValueAsString(Object value) { return \"\"; }\n" +
			"    public enum DefaultTyping { NON_FINAL }\n" +
			"    public static class DefaultTypeResolverBuilder {\n        public boolean useForType(Object t) { return true; }\n    }\n}\n",
		base + "JsonNode.java": "package com.fasterxml.jackson.databind;\n\npublic abstract class JsonNode {\n    public JsonNode get(int i) { return null; }\n}\n",
		base + "json/JsonMapper.java": "package com.fasterxml.jackson.databind.json;\n\nimport com.fasterxml.jackson.databind.ObjectMapper;\n\n" +
			"public class JsonMapper extends ObjectMapper {\n    public JsonMapper() { super(); }\n}\n",
		base + "deser/std/FromStringDeserializer.java": "package com.fasterxml.jackson.databind.deser.std;\n\npublic class FromStringDeserializer {\n" +
			"    public static class Std {\n        protected Object _deserialize(String value) { return 1; }\n    }\n}\n",
		base + "ext/CoreXMLDeserializers.java": "package com.fasterxml.jackson.databind.ext;\n\npublic class CoreXMLDeserializers {\n" +
			"    public static class Std {\n        protected Object _deserialize(String value) { return 2; }\n    }\n}\n",
		"src/main/java/org/apache/commons/lang3/Streams.java":        "package org.apache.commons.lang3;\n\n@Deprecated\npublic class Streams {\n    public static int stream() { return 1; }\n}\n",
		"src/main/java/org/apache/commons/lang3/stream/Streams.java": "package org.apache.commons.lang3.stream;\n\npublic class Streams {\n    public static int stream() { return 2; }\n}\n",
	})
}

func tsNamesRepo(t *testing.T) *Handler {
	return namesRepo(t, "typeorm", "", map[string]string{
		"src/repository/Repository.ts":                      "export class Repository<Entity> {\n    find(options?: any): Entity[] {\n        return []\n    }\n}\n",
		"src/repository/TreeRepository.ts":                  "import { Repository } from \"./Repository\"\n\nexport class TreeRepository<Entity> extends Repository<Entity> {\n    findTrees(): Entity[] {\n        return []\n    }\n}\n",
		"src/repository/BaseEntity.ts":                      "export class BaseEntity {\n    static getRepository(): any {\n        return null\n    }\n}\n",
		"src/globals.ts":                                    "export function getRepository(target: any): any {\n    return null\n}\n\nexport function createQueryBuilder(): any {\n    return null\n}\n",
		"src/container.ts":                                  "export function useContainer(c: any): void {}\n\nexport function getFromContainer(c: any): any {\n    return null\n}\n",
		"src/data-source/DataSource.ts":                     "export class DataSource {\n    isInitialized = false\n    createQueryBuilder(): any {\n        return null\n    }\n}\n",
		"src/query-builder/RelationIdLoader.ts":             "export class RelationIdLoader {\n    load(): number {\n        return 1\n    }\n}\n",
		"src/query-builder/relation-id/RelationIdLoader.ts": "export class RelationIdLoader {\n    load(): number {\n        return 2\n    }\n}\n",
	})
}

func jsNamesRepo(t *testing.T) *Handler {
	return namesRepo(t, "express", "", map[string]string{
		"lib/response.js":                       "var http = require('http')\nvar res = Object.create(http.ServerResponse.prototype)\nmodule.exports = res\n\nres.json = function json(obj) {\n  return this.send(obj)\n}\n",
		"lib/application.js":                    "var app = exports = module.exports = {};\n\napp.render = function render(name, options, callback) {\n  return 1\n}\n\napp.listen = function listen() {\n  return 2\n}\n\nfunction logerror(err) {\n  console.error(err)\n}\n",
		"lib/view.js":                           "function View(name, options) {\n  this.name = name\n}\n\nView.prototype.render = function render(options, callback) {\n  return 3\n}\n\nmodule.exports = View\n",
		"lib/express.js":                        "function createApplication() {\n  return {}\n}\n\nexports = module.exports = createApplication\n",
		"examples/content-negotiation/users.js": "exports.json = function(req, res){\n  res.json([])\n};\n",
		"test/app.engine.js":                    "function render(path, options, fn) {\n  fn(null, path)\n}\n",
	})
}

func pyNamesRepo(t *testing.T) *Handler {
	return namesRepo(t, "flask", "", map[string]string{
		"src/flask/__init__.py":        "from .helpers import url_for\nfrom .json import jsonify\n",
		"src/flask/app.py":             "from .sansio.app import App\n\n\nclass Flask(App):\n    def run(self):\n        return 1\n\n    def url_for(self, endpoint):\n        return 2\n",
		"src/flask/sansio/app.py":      "from .scaffold import Scaffold\n\n\nclass App(Scaffold):\n    def name(self):\n        return 'app'\n",
		"src/flask/sansio/scaffold.py": "class Scaffold:\n    def route(self, rule):\n        return rule\n",
		"src/flask/helpers.py":         "def url_for(endpoint):\n    return endpoint\n",
		"src/flask/json/__init__.py":   "def jsonify(*args):\n    return args\n",
	})
}

func cNamesRepo(t *testing.T) *Handler {
	return namesRepo(t, "jansson", "", map[string]string{
		"src/lookup3.h":   "#include <stddef.h>\nstatic unsigned key(const void *k, size_t length) {\n    return 0;\n}\n",
		"src/jansson.h":   "typedef struct json_t {\n    int type;\n} json_t;\n\njson_t *json_object_get(const json_t *object, const char *key);\n",
		"src/value.c":     "#include \"jansson.h\"\n\njson_t *json_object_get(const json_t *json, const char *key) {\n    return 0;\n}\n",
		"src/hashtable.h": "struct hashtable_pair {\n    char *key;\n};\n",
	})
}

// TestLookupNames_NativeSpellings: "::", "->", "\", "#", "$", "+", backtick
// arity, call parens, signatures, generics, ".prototype." and Go receiver
// syntax all used to fail ("no symbol named") on symbols that exist.
func TestLookupNames_NativeSpellings(t *testing.T) {
	t.Run("go", func(t *testing.T) {
		runNameCases(t, ginNamesRepo(t), []nameCase{
			{"Context.JSON()", "Context.JSON@context.go"},
			{"Context.JSON(code int, obj any)", "Context.JSON@context.go"},
			{"Context.JSON[T]", "Context.JSON@context.go"},
			{"Recovery()", "Recovery@recovery.go"},
			{"Context.Errors", "Context.Errors@context.go"},
		})
	})
	t.Run("java", func(t *testing.T) {
		runNameCases(t, javaNamesRepo(t), []nameCase{
			{"ObjectMapper#writeValueAsString", "ObjectMapper.writeValueAsString@ObjectMapper.java"},
			{"ObjectMapper.writeValueAsString(Object)", "ObjectMapper.writeValueAsString@ObjectMapper.java"},
			{"ObjectMapper.writeValueAsString()", "ObjectMapper.writeValueAsString@ObjectMapper.java"},
			{"ObjectMapper.readValue<T>", "ObjectMapper.readValue@ObjectMapper.java"},
			{"ObjectMapper$DefaultTyping", "ObjectMapper.DefaultTyping@ObjectMapper.java"},
			{"ObjectMapper$DefaultTypeResolverBuilder.useForType", "ObjectMapper.DefaultTypeResolverBuilder.useForType@ObjectMapper.java"},
			{"ObjectMapper.<init>", "ObjectMapper.ObjectMapper@ObjectMapper.java"},
		})
	})
	t.Run("typescript", func(t *testing.T) {
		runNameCases(t, tsNamesRepo(t), []nameCase{
			{"Repository<T>", "Repository@src/repository/Repository.ts"},
			{"Repository<Entity>.find", "Repository.find@src/repository/Repository.ts"},
			{"Repository.prototype.find", "Repository.find@src/repository/Repository.ts"},
			{"Repository.find()", "Repository.find@src/repository/Repository.ts"},
		})
	})
	t.Run("cpp", func(t *testing.T) {
		runNameCases(t, namesRepo(t, "cppfx", "cppfx", nil), []nameCase{
			{"Rect::area", "geo::Rect::area@shapes.hpp"},
			{"Rect::w", "geo::Rect::w@shapes.hpp"},
			{"Shape::~Shape", "geo::Shape::~Shape@shapes.hpp"},
			{"::helper", "helper@shapes.cpp"},
			{"Circle", "geo::Circle@shapes.hpp"},
		})
	})
	t.Run("rust", func(t *testing.T) {
		runNameCases(t, namesRepo(t, "rustfx", "rustfx", nil), []nameCase{
			{"Client::get", "Client.get@src/net/client.rs"},
			{"Config::new()", "Config.new@src/lib.rs"},
			{"crate::net::client::Client::new", "Client.new@src/net/client.rs"},
			{"crate::net::connect", "connect@src/net/mod.rs"},
			{"rustfx::parse_config", "parse_config@src/lib.rs"},
			{"<MemStore as Store>::get", "MemStore.get@src/lib.rs"},
			{"Cache::<T>::get", "Cache.get@src/lib.rs"},
			{"Cache<T>::get", "Cache.get@src/lib.rs"},
		})
	})
	t.Run("php", func(t *testing.T) {
		h := namesRepo(t, "php-semver", "", map[string]string{
			"src/Constraint/Constraint.php": "<?php\n\nnamespace Composer\\Semver\\Constraint;\n\nclass Constraint\n{\n    const OP_EQ = 0;\n    protected $version;\n\n    public function matches($provider)\n    {\n        return true;\n    }\n}\n",
			"src/Comparator.php":            "<?php\n\nnamespace Composer\\Semver;\n\nclass Comparator\n{\n    public static function compare($a, $op, $b)\n    {\n        return true;\n    }\n}\n",
		})
		runNameCases(t, h, []nameCase{
			{"Constraint::matches", "Constraint.matches@Constraint.php"},
			{"Constraint->matches", "Constraint.matches@Constraint.php"},
			{"Constraint::matches()", "Constraint.matches@Constraint.php"},
			{`\Composer\Semver\Constraint\Constraint::matches`, "Constraint.matches@Constraint.php"},
			{`Composer\Semver\Comparator::compare`, "Comparator.compare@Comparator.php"},
			{`Composer\Semver\Comparator`, "Comparator@Comparator.php"},
			{"Constraint::OP_EQ", "Constraint.OP_EQ@Constraint.php"},
			{"Constraint::$version", "Constraint.version@Constraint.php"},
			{`\Composer\Semver\Comparator::matches`, ""},
			{`Other\Ns\Constraint::matches`, ""},
		})
	})
	t.Run("csharp-kotlin-swift", func(t *testing.T) {
		runNameCases(t, namesRepo(t, "csfx", "csfx", nil), []nameCase{
			{"Cart+Line.Render", "Cart.Line.Render@Shop/Cart.cs"},
			{"Repo`1.Find", "Repo.Find@Shop/Cart.cs"},
			{"Repo<T>.Find", "Repo.Find@Shop/Cart.cs"},
			{"global::Acme.Shop.Cart.Get", "Cart.Get@Shop/Cart.cs"},
			{"Other.Shop.Cart.Get", ""},
		})
		runNameCases(t, namesRepo(t, "ktfx", "ktfx", nil), []nameCase{
			{"Cart.Companion.empty", "Cart.empty@Cart.kt"},
			{"com.acme.shop.Cart.get", "Cart.get@Cart.kt"},
			{"CartKt.topLevelHelper", "topLevelHelper@Cart.kt"},
		})
		runNameCases(t, namesRepo(t, "swiftfx", "swiftfx", nil), []nameCase{
			{"User.get(key:)", "User.get@Model.swift"},
			{"App.User.greet", "User.greet@Model.swift"},
		})
	})
	t.Run("c", func(t *testing.T) {
		runNameCases(t, cNamesRepo(t), []nameCase{
			{"struct json_t", "json_t@src/jansson.h"},
		})
	})
}

// TestLookupNames_PackageAndPathQualifiers: a package, module, namespace or
// path qualifier selects the file — and one that contradicts the symbol's
// location is a NO EXACT MATCH, never silently ignored.
func TestLookupNames_PackageAndPathQualifiers(t *testing.T) {
	t.Run("go", func(t *testing.T) {
		runNameCases(t, ginNamesRepo(t), []nameCase{
			{"gin.Default", "Default@gin.go"},
			{"github.com/gin-gonic/gin.Default", "Default@gin.go"},
			{"binding.Default", "Default@binding/binding.go"},
			{"github.com/gin-gonic/gin/binding.Default", "Default@binding/binding.go"},
			{"gin/render.JSON", "JSON@render/json.go"},
			{"render/json.go.JSON", "JSON@render/json.go"},
			{"render.JSON", "JSON@render/json.go"},
			{"gin.Context.JSON", "Context.JSON@context.go"},
			{"github.com/gin-gonic/gin.Context.JSON", "Context.JSON@context.go"},
			{"github.com/other/pkg.Context.JSON", ""},
		})
	})
	t.Run("java", func(t *testing.T) {
		runNameCases(t, javaNamesRepo(t), []nameCase{
			{"com.fasterxml.jackson.databind.ObjectMapper", "ObjectMapper@databind/ObjectMapper.java"},
			{"databind.ObjectMapper", "ObjectMapper@databind/ObjectMapper.java"},
			{"com.fasterxml.jackson.databind.ObjectMapper.readValue", "ObjectMapper.readValue@ObjectMapper.java"},
			{"com/fasterxml/jackson/databind/ObjectMapper.readValue", "ObjectMapper.readValue@ObjectMapper.java"},
			{"org.apache.commons.lang3.stream.Streams", "Streams@lang3/stream/Streams.java"},
			{"org.apache.commons.lang3.Streams", "Streams@lang3/Streams.java"},
			{"stream.Streams", "Streams@lang3/stream/Streams.java"},
			{"org.apache.commons.lang3.stream.Streams.stream", "Streams.stream@lang3/stream/Streams.java"},
			{"src/main/java/org/apache/commons/lang3/stream/Streams.java", "Streams@lang3/stream/Streams.java"},
			{"CoreXMLDeserializers.Std._deserialize", "CoreXMLDeserializers.Std._deserialize@CoreXMLDeserializers.java"},
			{"FromStringDeserializer.Std._deserialize", "FromStringDeserializer.Std._deserialize@FromStringDeserializer.java"},
			{"Streams", ""}, // two classes: ambiguous, not a guess
		})
	})
	t.Run("typescript", func(t *testing.T) {
		runNameCases(t, tsNamesRepo(t), []nameCase{
			{"query-builder/relation-id/RelationIdLoader.load", "RelationIdLoader.load@relation-id/RelationIdLoader.ts"},
			{"relation-id.RelationIdLoader.load", "RelationIdLoader.load@relation-id/RelationIdLoader.ts"},
			{"./relation-id/RelationIdLoader", "RelationIdLoader@relation-id/RelationIdLoader.ts"},
			{"src/globals.createQueryBuilder", "createQueryBuilder@src/globals.ts"},
			{"src/globals.ts.createQueryBuilder", "createQueryBuilder@src/globals.ts"},
			{"typeorm.DataSource", "DataSource@src/data-source/DataSource.ts"},
			{"RelationIdLoader.load", ""},
		})
	})
	t.Run("python", func(t *testing.T) {
		runNameCases(t, pyNamesRepo(t), []nameCase{
			{"flask.url_for", "url_for@src/flask/helpers.py"},
			{"flask.helpers.url_for", "url_for@src/flask/helpers.py"},
			{"flask.json.jsonify", "jsonify@src/flask/json/__init__.py"},
			{"src/flask/helpers.py:url_for", "url_for@src/flask/helpers.py"},
			{"src/flask/helpers.py::url_for", "url_for@src/flask/helpers.py"},
			{"flask.helpers:url_for", "url_for@src/flask/helpers.py"},
			{"flask.app.Flask.run", "Flask.run@src/flask/app.py"},
			{"Flask.url_for", "Flask.url_for@src/flask/app.py"},
			{"url_for", "url_for@src/flask/helpers.py"},
		})
	})
	t.Run("c", func(t *testing.T) {
		runNameCases(t, cNamesRepo(t), []nameCase{
			{"value.c.json_object_get", "json_object_get@src/value.c"},
			{"src/value.json_object_get", "json_object_get@src/value.c"},
		})
	})
}

// TestLookupNames_InheritedMembers: Type.member where the member is declared
// on a supertype resolves through extends/implements edges, labeled.
func TestLookupNames_InheritedMembers(t *testing.T) {
	check := func(t *testing.T, h *Handler, q, want, via string) {
		t.Helper()
		runNameCases(t, h, []nameCase{{q, want}})
		out, _ := h.Invoke("prism_lookup", map[string]any{"name": q})
		m := out.(map[string]any)
		note, _ := m["note"].(string)
		if m["matchKind"] != "inherited" || !strings.Contains(note, via) {
			t.Errorf("%s: inherited resolution must be labeled (matchKind=%v note=%q)", q, m["matchKind"], note)
		}
	}
	check(t, pyNamesRepo(t), "Flask.route", "Scaffold.route@scaffold.py", "Flask -> App -> Scaffold")
	check(t, tsNamesRepo(t), "TreeRepository.find", "Repository.find@src/repository/Repository.ts", "TreeRepository -> Repository")
	check(t, javaNamesRepo(t), "JsonMapper.readValue", "ObjectMapper.readValue@ObjectMapper.java", "JsonMapper -> ObjectMapper")
	check(t, ginNamesRepo(t), "Engine.GET", "RouterGroup.GET@routergroup.go", "Engine -> RouterGroup")
	// A member no supertype declares stays unresolved.
	runNameCases(t, pyNamesRepo(t), []nameCase{{"Flask.frobnicate", ""}, {"App.run", ""}})
}

// TestLookupNames_TypoSuggestions: nearbyNames used to rerun the search that
// had just failed, so a misspelling never produced a suggestion.
func TestLookupNames_TypoSuggestions(t *testing.T) {
	h := ginNamesRepo(t)
	for q, want := range map[string]string{
		"Recovey":        "Recovery (recovery.go",
		"Context.JOSN":   "Context.JSON (context.go",
		"Context.Strnig": "Context.String (context.go",
	} {
		out, err := h.Invoke("prism_lookup", map[string]any{"name": q})
		if err != nil {
			t.Fatal(err)
		}
		m := out.(map[string]any)
		cands, _ := m["candidates"].([]string)
		if m["matched"] != false || len(cands) == 0 || !strings.HasPrefix(cands[0], want) {
			t.Errorf("%s: want first suggestion %q, got matched=%v %v", q, want, m["matched"], cands)
		}
	}
	// A misspelled owner still lists the right member among the candidates.
	out, _ := h.Invoke("prism_lookup", map[string]any{"name": "RouterGrop.GET"})
	if cands, _ := out.(map[string]any)["candidates"].([]string); !strings.Contains(strings.Join(cands, "\n"), "RouterGroup.GET (routergroup.go") {
		t.Errorf("RouterGrop.GET: RouterGroup.GET missing from candidates %v", cands)
	}
	out, _ = h.Invoke("prism_lookup", map[string]any{"name": "FrobnicateWidget"})
	if cands := out.(map[string]any)["candidates"]; cands != nil {
		t.Errorf("an unrelated name must not get suggestions: %v", cands)
	}
}

// TestLookupNames_CaseInsensitiveFallback: a case variant resolves only when
// no exact-case symbol exists, and says so.
func TestLookupNames_CaseInsensitiveFallback(t *testing.T) {
	h := namesRepo(t, "casefx", "", map[string]string{
		"a.go": "package p\n\ntype Context struct{}\n\nfunc (c *Context) JSON() {}\n\nfunc Recovery() {}\n",
		"b.go": "package p\n\nvar json = 1\n\nfunc Exact() {}\n\nfunc exact() {}\n",
	})
	runNameCases(t, h, []nameCase{
		{"context.json", "Context.JSON@a.go"},
		{"recovery", "Recovery@a.go"},
		{"Exact", "Exact@b.go"},
		{"exact", "exact@b.go"},
	})
	for q, wantFold := range map[string]bool{"context.json": true, "recovery": true, "Exact": false} {
		out, _ := h.Invoke("prism_lookup", map[string]any{"name": q})
		m := out.(map[string]any)
		note, _ := m["note"].(string)
		folded := m["matchKind"] == "case-insensitive" && strings.Contains(note, "case-insensitive")
		if folded != wantFold {
			t.Errorf("%s: case-insensitive label = %v, want %v (%v)", q, folded, wantFold, m)
		}
	}
}

// TestLookupNames_NoExactMatchDeliversNoUnrelatedBody: a NO EXACT MATCH
// answer lists candidates with signatures and flags the miss before
// anything else; an unrelated body is not delivered.
func TestLookupNames_NoExactMatchDeliversNoUnrelatedBody(t *testing.T) {
	h := ginNamesRepo(t)
	out, err := h.Invoke("prism_lookup", map[string]any{"name": "Context.Recovery"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["matched"] != false {
		t.Fatalf("want matched=false: %v", m)
	}
	if c, _ := m["content"].(string); c != "" {
		t.Errorf("unrelated body delivered: %q", c)
	}
	cands, _ := m["candidates"].([]string)
	if len(cands) == 0 || !strings.Contains(cands[0], "Recovery (recovery.go") || !strings.Contains(cands[0], "func Recovery()") {
		t.Errorf("candidates must lead with the rejected symbol and its signature: %v", cands)
	}
	text, ok := renderLookupAsText(m)
	if !ok || !strings.HasPrefix(text, "// NO EXACT MATCH") {
		t.Errorf("text must open with the flag:\n%s", text)
	}
}
