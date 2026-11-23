package mcp

import (
	"reflect"
	"testing"
)

// GAP-06: targets in each runner's addressing, never command lines.
func TestTestPlanTargets(t *testing.T) {
	sym := func(name, file, lang string) *testPlanSymbol {
		return &testPlanSymbol{Name: name, File: file, Language: lang}
	}
	out := map[string]any{
		"candidates": []testPlanTarget{
			{Symbol: sym("TestTracker", "internal/session/tracker_test.go", "go")},
			{Symbol: sym("TestDrift", "internal/session/drift_test.go", "go")},
			{Symbol: sym("helper", "internal/session/drift_test.go", "go")},
			{Symbol: sym("TestShape.test_area", "tests/test_shapes.py", "python")},
			{Symbol: sym("ShapeTest.testArea", "src/test/java/ShapeTest.java", "java")},
			{Symbol: sym("tests.area", "src/shapes.rs", "rust")},
		},
		"fileTargets": []testPlanTarget{{File: "web/app.test.ts"}, {File: "tests/test_shapes.py"}},
	}
	got := TestPlanTargets(out)
	want := []string{
		"tests/test_shapes.py::TestShape::test_area",
		"ShapeTest#testArea",
		"shapes::tests::area",
		"./internal/session -run '^(TestDrift|TestTracker)$'",
		"web/app.test.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets:\n%q\nwant\n%q", got, want)
	}
}
