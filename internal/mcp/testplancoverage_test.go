package mcp

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func covChanges() []planChange {
	return []planChange{{File: "calc/calc.go", Name: "Add", After: &[2]int{3, 5}}, {File: "calc/calc.go", Name: "Unused", After: &[2]int{7, 12}}}
}

func covSymbol(t *testing.T, pc planCoverage, name string) planCoveredSymbol {
	t.Helper()
	for _, s := range pc.Symbols {
		if s.Symbol == name {
			return s
		}
	}
	t.Fatalf("no %s in %+v", name, pc)
	return planCoveredSymbol{}
}

func TestCoverageGoProfile(t *testing.T) {
	root := t.TempDir()
	covWrite(t, root, "calc/calc.go", "x\n")
	covWrite(t, root, "coverage.out", "mode: set\nexample.com/m/calc/calc.go:3.24,5.2 1 1\nexample.com/m/calc/calc.go:7.24,12.2 3 0\nexample.com/m/other/o.go:1.1,2.2 1 1\n")
	// Recorded after the edit: current line numbers.
	future := filepath.Join(root, "coverage.out")
	_ = os.Chtimes(future, nowPlus(10), nowPlus(10))
	cov, files, gaps := planCoverageReport(root, nil, covChanges(), func(string) string { return "" })
	if len(gaps) != 0 || len(cov) != 1 || len(files) != 0 {
		t.Fatalf("cov=%+v files=%v gaps=%v", cov, files, gaps)
	}
	if a := covSymbol(t, cov[0], "Add"); a.Executed != 3 || a.Instrumented != 3 {
		t.Fatalf("Add: %+v", a)
	}
	if u := covSymbol(t, cov[0], "Unused"); u.Executed != 0 || u.Instrumented != 6 {
		t.Fatalf("Unused: %+v", u)
	}
	if cov[0].Format != "go" || cov[0].PerTest || cov[0].Matched != "current" {
		t.Fatalf("header: %+v", cov[0])
	}
}

func TestCoverageLcovPerTest(t *testing.T) {
	root := t.TempDir()
	covWrite(t, root, "calc/calc.go", "x\n")
	covWrite(t, root, "lcov.info", "TN:tests/test_a.py::test_add\nSF:"+filepath.Join(root, "calc/calc.go")+"\nDA:4,1\nDA:8,0\nend_of_record\nTN:tests/test_b.py::test_other\nSF:calc/calc.go\nDA:9,2\nend_of_record\n")
	_ = os.Chtimes(filepath.Join(root, "lcov.info"), nowPlus(10), nowPlus(10))
	cov, files, _ := planCoverageReport(root, nil, covChanges(), func(n string) string { return coverageTestFile(n, nil) })
	if !cov[0].PerTest || !reflect.DeepEqual(covSymbol(t, cov[0], "Add").Tests, []string{"tests/test_a.py::test_add"}) {
		t.Fatalf("lcov: %+v", cov[0])
	}
	if _, ok := files["tests/test_b.py"]; !ok {
		t.Fatalf("test files: %v", files)
	}
}

func TestCoverageJaCoCoAndBaseLines(t *testing.T) {
	root := t.TempDir()
	covWrite(t, root, "src/main/java/com/x/Calc.java", "x\n")
	covWrite(t, root, "target/site/jacoco/jacoco.xml", `<?xml version="1.0"?><report name="r"><package name="com/x"><sourcefile name="Calc.java"><line nr="10" mi="0" ci="2"/><line nr="11" mi="3" ci="0"/></sourcefile></package></report>`)
	// Older than the edited file: matched against the base span.
	_ = os.Chtimes(filepath.Join(root, "target/site/jacoco/jacoco.xml"), nowPlus(-100), nowPlus(-100))
	ch := []planChange{{File: "src/main/java/com/x/Calc.java", Name: "Calc.add", Before: &[2]int{10, 11}, After: &[2]int{20, 21}}}
	cov, _, _ := planCoverageReport(root, nil, ch, func(string) string { return "" })
	s := covSymbol(t, cov[0], "Calc.add")
	if cov[0].Format != "jacoco" || cov[0].Matched != "base" || s.Instrumented != 2 || s.Executed != 1 {
		t.Fatalf("jacoco: %+v %+v", cov[0], s)
	}
}

func TestCoveragePyContexts(t *testing.T) {
	root := t.TempDir()
	covWrite(t, root, "shop/cart.py", "x\n")
	db, err := sql.Open("sqlite", filepath.Join(root, ".coverage"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE file (id INTEGER PRIMARY KEY, path TEXT)`,
		`CREATE TABLE context (id INTEGER PRIMARY KEY, context TEXT)`,
		`CREATE TABLE line_bits (file_id INTEGER, context_id INTEGER, numbits BLOB)`,
		`INSERT INTO file VALUES (1, 'shop/cart.py')`,
		`INSERT INTO context VALUES (1, 'tests.test_registry.test_dynamic')`,
		`INSERT INTO line_bits VALUES (1, 1, X'60')`, // lines 5 and 6
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	_ = os.Chtimes(filepath.Join(root, ".coverage"), nowPlus(10), nowPlus(10))
	known := map[string]bool{"tests/test_registry.py": true, "shop/cart.py": true}
	ch := []planChange{{File: "shop/cart.py", Name: "discount", After: &[2]int{5, 6}}}
	cov, files, gaps := planCoverageReport(root, nil, ch, func(n string) string { return coverageTestFile(n, known) })
	if len(gaps) > 0 || !cov[0].PerTest {
		t.Fatalf("cov=%+v gaps=%v", cov, gaps)
	}
	if s := covSymbol(t, cov[0], "discount"); s.Executed != 2 || s.Instrumented != -1 {
		t.Fatalf("discount: %+v", s)
	}
	if _, ok := files["tests/test_registry.py"]; !ok {
		t.Fatalf("context not mapped to its file: %v", files)
	}
}

func nowPlus(sec int) time.Time { return time.Now().Add(time.Duration(sec) * time.Second) }

func covWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
