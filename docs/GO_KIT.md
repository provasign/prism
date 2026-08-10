# Go library (`pkg/kit`)

`github.com/provasign/prism/pkg/kit` embeds Prism's engine in a Go program —
the same internals the CLI and MCP server use, behind a four-method facade.
Downstream agents (e.g. [mason](https://github.com/provasign/mason)) use it to
invoke tools in-process instead of shelling out.

One `Kit` is one Prism session against one repository root: the embedded
Grove engine, the in-process delivery cache (repeat reads collapse to SHA
pointers within the session), and the persistent per-root savings ledger —
shared with CLI invocations of the same repo.

```go
import "github.com/provasign/prism/pkg/kit"

k, err := kit.Open("/path/to/repo") // loads prism.yaml, opens the embedded
if err != nil {                     // engine, auto-indexes a never-indexed repo
    log.Fatal(err)
}
defer k.Close() // persists the savings ledger, shuts the engine down

// Invoke runs any tool by its MCP name — same dispatch as the MCP server
// and the HTTP API. Argument names match the tool's MCP inputSchema.
out, err := k.Invoke("prism_change_impact", map[string]any{
    "query": "ResponseWriter.Status",
})

res, err := k.Invoke("prism_query", map[string]any{
    "task":  "fix the ledger rollover",
    "terms": []string{"ledger"}, // REQUIRED for prism_query
})

// What symbols does one file define? (the "what did this task create" hook)
syms, err := k.FileSymbols(ctx, "internal/cli/commands.go")

// Persistent per-root token totals (shared with the CLI's `prism savings`)
s := k.Savings() // OriginalTokens, DeliveredTokens, SavedPercent
```

## API surface

| Symbol | Purpose |
|---|---|
| `Open(dir) (*Kit, error)` | Open a session: load `prism.yaml`, start the embedded engine, auto-index if the repo has never been indexed, attach the per-root ledger |
| `(*Kit).Invoke(tool, args) (any, error)` | Run one tool by MCP name (any of the 21 dispatchable tools); returns the raw result value |
| `(*Kit).FileSymbols(ctx, relPath)` | The engine's indexed symbols for one repo-relative file, as plain data (`Name`, `QualifiedName`, `Kind`, `Line`) |
| `(*Kit).Savings()` | Ledger totals for this root as plain data |
| `(*Kit).Root()` | The absolute repository root the Kit is bound to |
| `(*Kit).Close()` | Persist the ledger and shut down the engine |

## Notes

- **Results are `any`** — the same JSON-shaped values the MCP server returns.
  Type-assert to `map[string]any` / marshal as needed; there are no typed
  result structs at this layer.
- **One Kit per root.** Open a second Kit for a second repository.
- **The ledger is shared** with CLI runs of the same repo (same per-root file
  under the user cache dir, retained ~30 days), so `prism savings .` reflects
  Kit sessions too.
- Full reference: [pkg.go.dev/github.com/provasign/prism/pkg/kit](https://pkg.go.dev/github.com/provasign/prism/pkg/kit)
  (regenerates at each release tag).
