package embeddings

import (
	"testing"

	"github.com/tabladrum/grove-suite/prism/internal/grove"
)

func TestTFIDFRanksRelevantFirst(t *testing.T) {
	tf := NewTFIDF()
	corpus := []grove.SymbolRecord{
		{ID: "a", Name: "BuildEdges", Docstring: "build edges graph traversal symbol"},
		{ID: "b", Name: "Coffee", Docstring: "completely unrelated coffee tea"},
		{ID: "c", Name: "BuildEdgeMap", Docstring: "build edges between nodes"},
	}
	tf.Index(corpus)
	hits := tf.Query("build edges", corpus, 0)
	if len(hits) < 2 {
		t.Fatalf("expected at least 2 hits, got %d", len(hits))
	}
	if hits[0].Symbol.ID == "b" {
		t.Fatalf("unrelated doc should not rank first: %+v", hits)
	}
}

func TestTFIDFEmptyQuery(t *testing.T) {
	tf := NewTFIDF()
	corpus := []grove.SymbolRecord{{ID: "a", Name: "Hello"}}
	tf.Index(corpus)
	if hits := tf.Query("", corpus, 5); len(hits) != 0 {
		t.Fatalf("empty query should return no hits, got %d", len(hits))
	}
}
