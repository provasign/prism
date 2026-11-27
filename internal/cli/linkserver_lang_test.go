package cli

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/prism/internal/linkserver"
	"github.com/provasign/prism/internal/testtool"
)

// The link server across languages: a consumer that is not on this machine
// uploads its bundle, and the library's change-impact lists the consumer's
// site, as TestWorkspaceServerAnswersForRepositoriesNotCheckedOut does for
// Go. Each consumer is built the way its CI would build it, offline, so its
// references to the library are resolved by the compiler where the language
// has one.

// linkServerFixture starts a link server behind the token the client reads.
func linkServerFixture(t *testing.T) (base, url string) {
	t.Helper()
	base, _ = filepath.EvalSymlinks(t.TempDir())
	t.Setenv("PRISM_STATE_DIR", filepath.Join(base, "state"))
	store, err := linkserver.Open(filepath.Join(base, "store"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(store.Handler("s3cret"))
	t.Cleanup(srv.Close)
	t.Setenv("PRISM_LINK_TOKEN", "s3cret")
	return base, srv.URL
}

// commitAll makes dir a git repository on main holding everything in it.
func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "consumer")
}

// pushConsumer exports the consumer's bundle (to read its keys), then
// pushes it, from the consumer's directory. It returns the bundle.
func pushConsumer(t *testing.T, app, server, repo string) linkserver.Bundle {
	t.Helper()
	t.Chdir(app)
	out := filepath.Join(filepath.Dir(app), "bundle.json")
	if rc := workspaceExport(false, []string{"-o", out, "--repo", repo}); rc != 0 {
		t.Fatalf("workspace export rc=%d", rc)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var b linkserver.Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Requires {
		t.Logf("bundle requires %s:%s %s (%s)", r.Ecosystem, r.Name, r.Version, r.Manifest)
	}
	for _, r := range b.Refs {
		t.Logf("bundle ref %s  %s:%d:%d [in %s]  %q", r.Key, r.File, r.Line, r.Col, r.From, r.Text)
	}
	if rc := workspaceExport(true, []string{"--server", server, "--repo", repo}); rc != 0 {
		t.Fatalf("workspace push rc=%d", rc)
	}
	return b
}

func bundleHasKey(b linkserver.Bundle, key string) bool {
	for _, r := range b.Refs {
		if r.Key == key {
			return true
		}
	}
	return false
}

func libraryImpact(t *testing.T, lib, symbol string) string {
	t.Helper()
	t.Chdir(lib)
	out := captureStdout(func() { cmdChangeImpact([]string{symbol, "--format", "text"}) })
	t.Logf("change-impact %s:\n%s", symbol, out)
	return out
}

func requireLines(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("change-impact output missing %q:\n%s", w, out)
		}
	}
}

// javaHome finds a JDK for the Maven runs the fixture makes itself.
func javaHome() string {
	if h := os.Getenv("JAVA_HOME"); h != "" {
		return h
	}
	if out, err := exec.Command("/usr/libexec/java_home").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	for _, pattern := range []string{"/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk/Contents/Home", "/usr/lib/jvm/*"} {
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

func runIn(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// Java: the library is installed into a local Maven repository of the
// test's own, and the consumer depends on it by coordinates, so javac
// resolves the call against the library's jar.
func TestLinkServerJavaConsumer(t *testing.T) {
	if !hasJDK() {
		t.Skip("no JDK")
	}
	testtool.Require(t, "mvn")
	testtool.Require(t, "git")
	home, _ := os.UserHomeDir()
	userRepo := filepath.Join(home, ".m2", "repository")
	if _, err := os.Stat(filepath.Join(userRepo, "org", "apache", "maven", "plugins", "maven-dependency-plugin")); err != nil {
		t.Skip("no maven-dependency-plugin in ~/.m2: the offline classpath cannot be resolved")
	}
	base, server := linkServerFixture(t)
	lib, app := filepath.Join(base, "lib"), filepath.Join(base, "app")
	m2 := filepath.Join(base, "m2")
	// The library's local repository, chained to the user's for Maven's own
	// plugins: nothing is downloaded and ~/.m2 is not written.
	mavenConfig := "-Dmaven.repo.local=" + m2 + "\n-Dmaven.repo.local.tail=" + userRepo + "\n"
	pom := func(artifact, deps string) string {
		return "<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId><artifactId>" + artifact +
			"</artifactId><version>1.0</version><properties><maven.compiler.release>17</maven.compiler.release>" +
			"<project.build.sourceEncoding>UTF-8</project.build.sourceEncoding></properties>" + deps + "</project>\n"
	}
	writeCLIFile(t, lib, "pom.xml", pom("lib", ""))
	writeCLIFile(t, lib, ".mvn/maven.config", mavenConfig)
	writeCLIFile(t, lib, "prism.yaml", "version: 2\nworkspace_server: "+server+"\n")
	writeCLIFile(t, lib, "src/main/java/com/acme/lib/Box.java", "package com.acme.lib;\n\npublic class Box {\n    public int open() { return 1; }\n}\n")
	writeCLIFile(t, app, "pom.xml", pom("app", "<dependencies><dependency><groupId>com.acme</groupId><artifactId>lib</artifactId><version>1.0</version></dependency></dependencies>"))
	writeCLIFile(t, app, ".mvn/maven.config", mavenConfig)
	writeCLIFile(t, app, ".gitignore", ".grove/\ntarget/\n")
	writeCLIFile(t, app, "prism.yaml", "version: 2\n")
	writeCLIFile(t, app, "src/main/java/com/acme/app/Main.java", "package com.acme.app;\n\nimport com.acme.lib.Box;\n\npublic class Main {\n    static int run(Box b) {\n        return b.open();\n    }\n}\n")
	var env []string
	if h := javaHome(); h != "" {
		env = append(env, "JAVA_HOME="+h)
	}
	runIn(t, lib, env, "mvn", "-o", "-q", "-B", "install")
	if _, err := os.Stat(filepath.Join(m2, "com", "acme", "lib", "1.0", "lib-1.0.jar")); err != nil {
		t.Fatalf("library jar not installed: %v", err)
	}
	commitAll(t, app)
	// The consumer's CI: a clean index (no --allow-heuristic), then push.
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d: the consumer's classpath did not resolve", rc)
	}
	// --allow-heuristic: the library has no dependencies, so Maven writes an
	// empty classpath, which the javac pass reports as "Maven classpath not
	// resolvable" (a readiness problem, exit 3). The library's own index is
	// not what this test is about.
	if rc := cmdIndex([]string{lib, "--allow-heuristic"}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	b := pushConsumer(t, app, server, "example.org/team/app")
	if !bundleHasKey(b, "jvm:com.acme.lib#Box.open") {
		t.Fatalf("the bundle has no compiler-resolved reference to jvm:com.acme.lib#Box.open: %+v", b.Refs)
	}
	out := libraryImpact(t, lib, "Box.open")
	requireLines(t, out,
		"consumers in other repositories",
		"// repo example.org/team/app@",
		"(main)  from the server, not checked out  requires com.acme:lib 1.0",
		"example.org/team/app:src/main/java/com/acme/app/Main.java:7:18: return b.open();  [in Main.run]",
		"1 site(s) in 1 of 1 uploaded repositories, each at its uploaded commit",
	)
}

// TypeScript: the consumer depends on the library through an npm file:
// dependency, installed offline with typescript from the npm cache, so the
// checker resolves the call into node_modules/@acme/lib.
func TestLinkServerTypeScriptConsumer(t *testing.T) {
	for _, tool := range []string{"node", "npm", "git"} {
		testtool.Require(t, tool)
	}
	base, server := linkServerFixture(t)
	lib, app := filepath.Join(base, "lib"), filepath.Join(base, "app")
	tsconfig := `{"compilerOptions":{"target":"ES2020","module":"commonjs","moduleResolution":"node","strict":true,"skipLibCheck":true}}` + "\n"
	writeCLIFile(t, lib, "package.json", `{"name":"@acme/lib","version":"1.0.0","main":"index.ts","types":"index.ts","devDependencies":{"typescript":"^5"}}`+"\n")
	writeCLIFile(t, lib, "tsconfig.json", tsconfig)
	writeCLIFile(t, lib, "prism.yaml", "version: 2\nworkspace_server: "+server+"\n")
	writeCLIFile(t, lib, "index.ts", "export class Box {\n  open(): number {\n    return 1;\n  }\n}\n\nexport function normalize(s: string): string {\n  return s.toLowerCase();\n}\n")
	writeCLIFile(t, app, "package.json", `{"name":"@acme/app","version":"1.0.0","dependencies":{"@acme/lib":"file:../lib"},"devDependencies":{"typescript":"^5"}}`+"\n")
	writeCLIFile(t, app, "tsconfig.json", tsconfig)
	writeCLIFile(t, app, ".gitignore", ".grove/\nnode_modules/\n")
	writeCLIFile(t, app, "prism.yaml", "version: 2\n")
	writeCLIFile(t, app, "src/main.ts", "import { Box, normalize } from \"@acme/lib\";\n\nexport function run(b: Box): string {\n  return normalize(String(b.open()));\n}\n")
	npm := []string{"install", "--offline", "--no-audit", "--no-fund", "--ignore-scripts"}
	cmd := exec.Command("npm", npm...)
	cmd.Dir = lib
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("typescript@5 is not in the npm cache, so it cannot be installed offline: %v\n%s", err, out)
	}
	runIn(t, app, nil, "npm", npm...)
	if _, err := os.Stat(filepath.Join(app, "node_modules", "@acme", "lib", "index.ts")); err != nil {
		t.Fatalf("the file: dependency is not installed: %v", err)
	}
	commitAll(t, app)
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	b := pushConsumer(t, app, server, "example.org/team/app")
	for _, key := range []string{"npm:@acme/lib#Box.open", "npm:@acme/lib#normalize"} {
		if !bundleHasKey(b, key) {
			t.Fatalf("the bundle has no compiler-resolved reference to %s: %+v", key, b.Refs)
		}
	}
	out := libraryImpact(t, lib, "Box.open")
	requireLines(t, out,
		"consumers in other repositories",
		"(main)  from the server, not checked out",
		"example.org/team/app:src/main.ts:4:",
		"return normalize(String(b.open()));  [in run]",
		"1 site(s) in 1 of 1 uploaded repositories, each at its uploaded commit",
	)
	out = libraryImpact(t, lib, "normalize")
	requireLines(t, out,
		"example.org/team/app:src/main.ts:4:10: return normalize(String(b.open()));  [in run]",
		"1 site(s) in 1 of 1 uploaded repositories, each at its uploaded commit",
	)
}

// Python: no compiler pass. The consumer's call through a name it imported
// from the library's package is recorded import-scoped, as for a linked
// checkout, and the server's answer must say so.
func TestLinkServerPythonConsumer(t *testing.T) {
	testtool.Require(t, "git")
	base, server := linkServerFixture(t)
	lib, app := filepath.Join(base, "lib"), filepath.Join(base, "app")
	writeCLIFile(t, lib, "pyproject.toml", "[project]\nname = \"extlib\"\nversion = \"1.0\"\n")
	writeCLIFile(t, lib, "prism.yaml", "version: 2\nworkspace_server: "+server+"\n")
	writeCLIFile(t, lib, "extlib/__init__.py", "from .models import persist\n")
	writeCLIFile(t, lib, "extlib/models.py", "def persist(m):\n    pass\n")
	writeCLIFile(t, app, "pyproject.toml", "[project]\nname = \"app\"\nversion = \"1.0\"\ndependencies = [\"extlib>=1.0\"]\n")
	writeCLIFile(t, app, ".gitignore", ".grove/\n")
	writeCLIFile(t, app, "prism.yaml", "version: 2\n")
	writeCLIFile(t, app, "app/use.py", "from extlib import persist\n\n\ndef run(m):\n    persist(m)\n")
	commitAll(t, app)
	if rc := cmdIndex([]string{app}); rc != 0 {
		t.Fatalf("index app rc=%d", rc)
	}
	if rc := cmdIndex([]string{lib}); rc != 0 {
		t.Fatalf("index lib rc=%d", rc)
	}
	b := pushConsumer(t, app, server, "example.org/team/app")
	if !bundleHasKey(b, "py:extlib#persist") {
		t.Fatalf("the bundle has no reference to py:extlib#persist: %+v", b.Refs)
	}
	out := libraryImpact(t, lib, "persist")
	requireLines(t, out,
		"consumers in other repositories",
		"(main)  from the server, not checked out",
		"example.org/team/app:app/use.py:5:5: persist(m)  [in run]",
		"1 site(s) in 1 of 1 uploaded repositories, each at its uploaded commit",
	)
	// The same evidence label a linked checkout's answer carries
	// (TestWorkspaceChangeImpactPython): no compiler pass, so the site is
	// import-scoped, not compiler-resolved. And the pin, though the key's
	// ecosystem (py) and the manifest's (pypi) are named differently.
	if !strings.Contains(out, "requires extlib >=1.0") {
		t.Fatalf("the server answer has no pin for the Python consumer:\n%s", out)
	}
	if !strings.Contains(out, "example.org/team/app:app/use.py:5:5: persist(m)  [in run]  [import-scoped]") {
		t.Fatalf("the server's Python site is not labelled import-scoped, so it reads as compiler-resolved:\n%s", out)
	}
}
