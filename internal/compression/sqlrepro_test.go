package compression

import (
	"strings"
	"testing"

	"github.com/provasign/prism/internal/grove"
	"github.com/provasign/prism/internal/session"
)

// sqlMigration is a realistic SQL file. Note that, as Grove typically models
// SQL, each statement's *signature* is just the header (e.g. "CREATE TABLE
// orders") while the meaningful content — the columns, constraints, the second
// index, the file comments — lives in RawText or between symbols.
const sqlMigration = `-- 0007_create_orders.sql
-- Adds the orders table and supporting indexes.

CREATE TABLE orders (
    id            BIGSERIAL PRIMARY KEY,
    customer_id   BIGINT NOT NULL REFERENCES customers(id),
    status        TEXT NOT NULL DEFAULT 'pending',
    total_cents   BIGINT NOT NULL CHECK (total_cents >= 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_customer ON orders (customer_id);
CREATE INDEX idx_orders_status ON orders (status) WHERE status <> 'archived';
`

func sqlSymbols() []grove.SymbolRecord {
	return []grove.SymbolRecord{
		{
			FilePath:  "migrations/0007_create_orders.sql",
			Name:      "orders",
			Kind:      "table",
			Language:  "sql",
			Signature: "CREATE TABLE orders",
			RawText: `CREATE TABLE orders (
    id            BIGSERIAL PRIMARY KEY,
    customer_id   BIGINT NOT NULL REFERENCES customers(id),
    status        TEXT NOT NULL DEFAULT 'pending',
    total_cents   BIGINT NOT NULL CHECK (total_cents >= 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);`,
			Span: grove.SpanInfo{Start: 4, End: 11},
		},
		{
			FilePath:  "migrations/0007_create_orders.sql",
			Name:      "idx_orders_customer",
			Kind:      "index",
			Language:  "sql",
			Signature: "CREATE INDEX idx_orders_customer",
			RawText:   "CREATE INDEX idx_orders_customer ON orders (customer_id);",
			Span:      grove.SpanInfo{Start: 13, End: 13},
		},
	}
}

// lowSimBackend returns a low score for everything — typical for SQL, whose DDL
// keywords rarely lexically match a natural-language task. This is exactly the
// condition under which the old ranker gutted SQL bodies to signatures.
type lowSimBackend struct{}

func (lowSimBackend) Similarity(task string, sym grove.SymbolRecord) float64 { return 0.03 }

// TestSQL_FirstReadIsFaithful is the regression guard for the reported
// "Prism compressed the SQL too much that it is not readable" failure. A first
// read must return the file byte-for-byte regardless of symbol relevance.
func TestSQL_FirstReadIsFaithful(t *testing.T) {
	opts := Options{
		Task:            "make the orders total column allow negative refunds",
		Symbols:         sqlSymbols(),
		Session:         session.NewTracker(100),
		Ledger:          session.NewLedger("sql"),
		TokenLedgerName: "prism_read",
		Confidence:      session.High,
		Embeddings:      lowSimBackend{},
	}

	r := CompressFileRead("migrations/0007_create_orders.sql", sqlMigration, opts)

	if r.Content != sqlMigration {
		t.Fatalf("first read of SQL must be byte-faithful.\nstrategy=%s\n--- got ---\n%s", r.Strategy, r.Content)
	}
	// Spot-check the specific content the old compressor dropped.
	for _, want := range []string{"total_cents", "CHECK (total_cents >= 0)", "idx_orders_status", "-- Adds the orders table"} {
		if !strings.Contains(r.Content, want) {
			t.Errorf("delivered SQL is missing %q", want)
		}
	}
}

// TestSQL_DeltaPreservesReadability verifies that even on the token-saving
// re-read (delta) path, an edited SQL file stays readable: changed statements
// and all inter-statement content are emitted verbatim, only unchanged bodies
// become recoverable pointers, and the pointer uses a valid SQL comment marker.
func TestSQL_DeltaPreservesReadability(t *testing.T) {
	tracker := session.NewTracker(100)
	base := Options{
		Symbols:         sqlSymbols(),
		Session:         tracker,
		Ledger:          session.NewLedger("sql"),
		TokenLedgerName: "prism_read",
		Confidence:      session.High,
	}

	// R1: faithful first read records per-symbol SHAs.
	CompressFileRead("migrations/0007_create_orders.sql", sqlMigration, base)

	// R2: the small customer index is edited; the large orders table (the bulk
	// of the file) is untouched. This is the case where a delta genuinely pays
	// off — the table body is pointered and only the changed index is re-sent.
	editedSyms := sqlSymbols()
	editedSyms[1].RawText = "CREATE INDEX idx_orders_customer ON orders (customer_id, status);"
	edited := strings.Replace(sqlMigration,
		"CREATE INDEX idx_orders_customer ON orders (customer_id);",
		"CREATE INDEX idx_orders_customer ON orders (customer_id, status);", 1)
	base.Symbols = editedSyms

	r := CompressFileRead("migrations/0007_create_orders.sql", edited, base)

	if r.Strategy != "semantic-delta" {
		t.Fatalf("edited SQL re-read: want semantic-delta, got %s\n%s", r.Strategy, r.Content)
	}
	// The changed index must be present verbatim (changed statement re-sent).
	if !strings.Contains(r.Content, "(customer_id, status)") {
		t.Errorf("changed index is missing its new definition:\n%s", r.Content)
	}
	// Inter-statement content must survive.
	if !strings.Contains(r.Content, "idx_orders_status") {
		t.Errorf("inter-symbol content dropped from delta:\n%s", r.Content)
	}
	// The unchanged table becomes a pointer using a SQL (--) comment, never //.
	if !strings.Contains(r.Content, "-- [prism:cached] orders") {
		t.Errorf("unchanged table should be a SQL-commented pointer:\n%s", r.Content)
	}
	if strings.Contains(r.Content, "// [prism:cached]") {
		t.Errorf("SQL pointer must not use a // comment marker:\n%s", r.Content)
	}
	if r.DeliveredTokens >= r.OriginalTokens {
		t.Errorf("delta should still save tokens: delivered=%d original=%d", r.DeliveredTokens, r.OriginalTokens)
	}
}
