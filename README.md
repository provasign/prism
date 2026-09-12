# Prism

**Semantic change intelligence for coding agents.** Prism turns repository structure into task-shaped answers: complete change-impact sets, edit-ready context, and deterministic verification of an agent-authored diff.

Prism embeds the [Grove](https://github.com/provasign/grove) code graph. It runs locally as one binary, exposes CLI and MCP interfaces, and requires no hosted service or API token.

## Why Prism

Text search is excellent for locating a name. It cannot reliably distinguish references from unrelated text, follow an override family, or enumerate types that implicitly satisfy an external interface. Prism combines full-text search with a typed project graph, then exposes the result through operations that match the question an agent is trying to answer.

| Need | Operation |
|---|---|
| Locate a symbol or exact text | `prism search` |
| Gather edit-ready source around known anchors | `prism query` |
| Read one file or symbol | `prism read` / `prism lookup` |
| Change a method signature | `prism change-impact` |
| Plan a rename | `prism rename-plan` |
| Evolve an interface | `prism missing-implementations` |
| Check an agent-authored diff | `prism verify` |
| Enforce component boundaries | `prism arch` |

Authoritative operations label the completeness of their answer. Unsupported, stale, or heuristic evidence is reported as such instead of being silently treated as complete.

## Current result

A September 2026 paired Sonnet sample compared native search/read tools with Prism on nine change-impact tasks in Go, Java, TypeScript, and Python. Each cell below is one fresh run, so this table is a release-gate snapshot rather than a variance estimate.

| Nine-task aggregate | Native tools | Prism |
|---|---:|---:|
| Mean recall | 0.683 | **0.998** |
| Mean precision | 0.770 | **0.955** |
| Total estimated cost | $3.23 | **$1.28** |
| Cost relative to native | 1.00× | **0.40×** |

Prism matched or improved recall and cost less in every task in this sample. The largest structural case was Grafana's externally declared `QueryData` interface: Prism found the complete 70-site local family in a deterministic call. Three fresh agent trials all reached 1.0 recall; precision ranged from 0.933 to 0.959.

Repeated trials matter because agent runs vary. The published evidence also includes three paired Jackson `JsonNode.get` trials: native tools averaged 0.625 recall, 30 turns, and $0.404; Prism averaged 1.0 recall, 8.3 turns, and $0.205. Full per-task results, raw records, oracles, scoring rules, and limitations live in [provasign/research](https://github.com/provasign/research).

## Install

```sh
# Homebrew (macOS or Linux)
brew install provasign/shale/prism

# macOS or Linux installer
curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash

# Windows PowerShell
irm https://raw.githubusercontent.com/provasign/prism/main/install.ps1 | iex

# Pin the current release
VERSION=v0.72.6 curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash
```

The installer writes to `~/bin` by default. Set `INSTALL_DIR` to choose another directory.
Use either Homebrew or the standalone installer as the authoritative installation. If both
are present with different versions, `prism init` and the MCP server report their paths.
The standalone installer stops running Prism MCP servers before replacing the binary,
verifies the installed version, and removes Prism registrations left by the old global
configuration model. After changing installation methods or upgrading Homebrew, run
`prism cleanup-global`, run `prism init` in each project, choose the harnesses to configure,
and restart the coding agent so pinned MCP paths and long-running servers refresh.

Build from source with `make build`; run the full test suite with `make test`.

## Quick start

From the root of a repository:

```sh
prism init .
```

This asks which harnesses you use (Claude Code, Codex, Cursor, Windsurf, VS Code,
Gemini, or opencode), then writes `prism.yaml`, the selected project-local MCP
configs, and their instruction files. For automation, pass a comma-separated list,
for example `prism init --harness claude,codex .`. Indexing happens automatically
on first use and refreshes incrementally after changes.

Use search to find an anchor, then ask the graph for the relationship you need:

```sh
prism search QueryData --scope text --exhaustive --files-only
prism change-impact 'QueryDataHandler.QueryData' --format text

prism query "fix request validation" \
  --terms ValidateRequest \
  --include graph \
  --format text

prism verify --base main --format text
```

For MCP-capable agents, `prism init` exposes six focused tools:

- `prism_search` locates symbols and source text. An exhaustive search returns a complete inventory of exact file paths and enclosing symbols while sampling context excerpts.
- `prism_query` returns budgeted, line-numbered source around named anchors, including graph neighbors and relevant tests.
- `prism_read` reads a file and deduplicates unchanged repeat reads within a session.
- `prism_lookup` returns one named symbol's complete body.
- `prism_change_impact` returns the declaration, override or implementation family, sibling contracts, and resolved callers. It can enumerate local implementations of an external Go interface method from its method set.
- `prism_verify` compares a diff with its required semantic change set and exits nonzero when known sites were missed.

CLI help is authoritative for the complete command and flag list:

```sh
prism --help
prism doctor .
```

## Workflow for coding agents

1. Locate the first anchor with `prism_search`; batch several known names into one call.
2. Before editing an existing symbol, run `prism_change_impact` and preserve the returned set.
3. Use `prism_query` for edit-ready context or `prism_lookup` for one complete body.
4. Use exhaustive text search for wide concept removals and other completeness questions.
5. Run tests and `prism_verify` before declaring a multi-site change complete.

`prism search --scope text` is a real repository text search. The graph adds value after location: callers, implementations, type relationships, tests, architectural edges, and completeness checks.

## Architecture and CI

Add boundary rules to `prism.yaml`:

```yaml
version: 1
arch_deny:
  - "internal/cli -> internal/mcp"
```

Then run:

```sh
prism map .
prism arch .
prism verify . --base main
```

`prism arch` reports the concrete `file:line` evidence for each forbidden component edge. `prism verify` checks the current diff for missed impact sites, new component dependencies, and introduced architecture violations.

## Language support

Prism indexes Go, TypeScript/TSX, JavaScript/JSX, Python, Java, Rust, C/C++, C#, and PHP. Semantic depth varies by language and by the native toolchain available in the repository. `prism doctor` reports the active analyzer and evidence tier rather than implying uniform compiler-level coverage.

## Interfaces

- **CLI:** local interactive use and CI
- **MCP over stdio:** Claude Code, Cursor, Windsurf, VS Code, Codex, and other MCP clients
- **HTTP:** `prism serve --port 8888`; see [docs/HTTP_API.md](docs/HTTP_API.md)
- **Go library:** see [docs/GO_KIT.md](docs/GO_KIT.md)

Grove is embedded in the Prism binary. Prism users do not need to install or run a separate graph service.

## Security and privacy

Prism analyzes the local working tree and stores its index locally. The default MCP transport is stdio. The optional HTTP server binds to loopback by default. See [SECURITY.md](SECURITY.md) and [THREAT_MODEL.md](THREAT_MODEL.md) for reporting and trust-boundary details.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md), and [SUPPORT.md](SUPPORT.md). Accuracy changes should include an oracle-backed regression case; public performance claims should link to reproducible evidence in [provasign/research](https://github.com/provasign/research).

## License

Apache License 2.0. See [LICENSE](LICENSE).
