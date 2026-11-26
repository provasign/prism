package core

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
)

const cartPy = "class Cart:\n    def total(self):\n        return 1\n\n    def size(self):\n        return 2\n\n\ndef checkout(cart):\n    return cart.total() + 1\n"

// BUG-13: renaming onto a name the owner already declares is a collision.
func TestRenamePlanReportsCollision(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{"shop.py": cartPy})
	out, err := h.Invoke("prism_rename_plan", map[string]any{"query": "Cart.total", "newName": "size"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	cols, _ := m["collisions"].([]map[string]any)
	if m["status"] != "collision" || len(cols) != 1 || cols[0]["name"] != "Cart.size" || cols[0]["line"] != 5 {
		t.Fatalf("collision not reported: %#v", m)
	}
	if _, ok := m["edits"]; ok {
		t.Fatalf("plan returned without allow_collision: %#v", m)
	}
	out, err = h.Invoke("prism_rename_plan", map[string]any{"query": "Cart.total", "newName": "size", "allow_collision": true})
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["status"] != "collision" || m["edits"] == nil {
		t.Fatalf("allow_collision did not return the plan: %#v", m)
	}
}

func TestRenamePlanNoCollisionKeepsIndentation(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{"shop.py": cartPy})
	out, err := h.Invoke("prism_rename_plan", map[string]any{"query": "Cart.total", "newName": "amount"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if _, ok := m["collisions"]; ok {
		t.Fatalf("unexpected collision: %#v", m)
	}
	found := false
	for _, e := range renameEdits(m["edits"]) {
		if e.Line == 2 {
			found = true
			if e.Before != "    def total(self):" || e.After != "    def amount(self):" {
				t.Fatalf("declaration edit lost its indentation: %q -> %q", e.Before, e.After)
			}
		}
	}
	if !found {
		t.Fatalf("no declaration edit: %#v", m["edits"])
	}
}

func TestRenamePlanGoFreeFunctionCollision(t *testing.T) {
	h := newImpactPlusHandler(t, map[string]string{
		"go.mod": "module example.com/c\n\ngo 1.22\n",
		"a.go":   "package c\n\nfunc Old() int { return 1 }\n\nfunc Use() int { return Old() }\n",
		"b.go":   "package c\n\nfunc Taken() int { return 2 }\n",
	})
	out, err := h.Invoke("prism_rename_plan", map[string]any{"query": "Old", "newName": "Taken"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if cols, _ := m["collisions"].([]map[string]any); m["status"] != "collision" || len(cols) != 1 || !strings.HasSuffix(cols[0]["file"].(string), "b.go") {
		t.Fatalf("same-package function not reported: %#v", m)
	}
}

func renameEdits(v any) []grove.RenameEdit {
	switch e := v.(type) {
	case []grove.RenameEdit:
		return e
	}
	return nil
}
