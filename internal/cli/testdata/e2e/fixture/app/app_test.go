package app

import (
	"testing"

	"example.com/fx/mem"
)

func TestLookup(t *testing.T) {
	c := mem.New()
	c.Put("k", "v")
	if got := Lookup(c, "k"); got != "v" {
		t.Fatalf("Lookup = %q", got)
	}
}
