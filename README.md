# Prism

**Semantic change intelligence for coding agents.** Prism turns indexed repository structure into task-shaped answers: potential change-impact sites, source context, and a review of an agent-authored diff.

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

Structural results report indexed coverage and known stale, partial, or heuristic evidence. A closed indexed result does not prove that every runtime caller or required edit is known.

## Benchmark

Reference benchmark: [docs/benchmarks/prism-v0.87.39-2026-10-21.md](docs/benchmarks/prism-v0.87.39-2026-10-21.md)
(2026-10-21). Claude Code with Sonnet 5.5, native tools vs Prism through the CLI
interface (`prism init --interface cli`). Same tasks, checkouts and tests in both
arms. Task cost is everything after the first request of a session, repriced at
Sonnet 5.5's 5-minute-cache prices.

**Change work.** 32 seeded tasks in Python, Java, TypeScript and Go: method
renames where the name is shared with unrelated methods, property renames, and
signature changes. Prism v0.87.39.

| | Native | Prism | Prism / native |
|---|---:|---:|---:|
| Solved | 31 / 32 | 32 / 32 | |
| Task cost per task | $0.0646 | $0.0492 | 0.76× |
| Tool calls per task | 6.8 | 4.4 | 0.65× |
| Startup per session | $0.0607 | $0.0626 | 1.03× |

**Bug fixes.** 40 GitHub issues filed after the model's training cutoff, in Go,
Java, TypeScript and Python projects. Measured on Prism v0.87.37; v0.87.38 and
v0.87.39 did not change the bug-fix path.

| | Native | Prism | Prism / native |
|---|---:|---:|---:|
| Solved | 23 / 40 | 25 / 40 | |
| Task cost per task | $0.0520 | $0.0477 | 0.92× |
| Tool calls per task | 6.85 | 5.85 | 0.85× |
| Startup per session | $0.0609 | $0.0626 | 1.03× |

The 25 vs 23 solve difference is within run-to-run noise. Read it as the same
result.

**Over a session.** Startup is paid once per session, task cost once per task.
Projected saving of Prism vs native for a session of N tasks of one kind:

| Tasks in session | Change work | Bug fixes |
|---:|---:|---:|
| 1 | 11% | 2% |
| 10 | 21% | 7% |
| 20 | 22% | 8% |
| 40 | 23% | 8% |

The projection does not model context growth within a session or count
Prism's one-time indexing. Limits: one model, one agent, one run per cell.
The fact sheet has per-language slices, engine accuracy per language, and links
to raw records in [provasign/research](https://github.com/provasign/research).

## Install

```sh
# Homebrew (macOS or Linux)
brew install provasign/shale/prism

# macOS or Linux installer
curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash

# Windows PowerShell
irm https://raw.githubusercontent.com/provasign/prism/main/install.ps1 | iex

# Pin the current release
VERSION=v0.87.39 curl -fsSL https://raw.githubusercontent.com/provasign/prism/main/install.sh | bash
```

The installer writes to `~/bin` by default. Set `INSTALL_DIR` to choose another directory.
Use either Homebrew or the standalone installer as the authoritative installation. If both
are present with different versions, `prism init` and the MCP server report their paths.
The standalone installer stops running Prism MCP servers before replacing the binary,
verifies the installed version, and removes Prism registrations left by the old global
configuration model. After changing installation methods or upgrading Homebrew, run
`prism cleanup-global`, run `prism init` in each project, choose the harnesses to configure,
and restart the coding agent so pinned MCP paths and long-running servers refresh.

The current release is v0.87.39. It embeds Grove v0.63.25 (Astkit v0.16.10).

Build from source with `make build`; run the full test suite with `make test`.

## Quick start

From the root of a repository:

```sh
prism init .
```

This detects installed and previously configured harnesses, recommends those as the
default, and asks which to configure. It then asks how agents should reach Prism:
**MCP** (the default) registers the compact project-local server; **CLI** registers
no MCP server and steers agents to run the `prism` command in their shell. Both
choices write `prism.yaml` and current instruction files. CLI init removes Prism's
own MCP registration from the selected harnesses' project configs if present.
The interactive prompt gives a source-file-size hint: CLI suits occasional calls,
while a resident MCP server is faster for repeated discovery, especially in a
large repository. It does not change the default or claim a model-cost saving.
Harness ids: `claude`, `codex`, `cursor`, `windsurf`, `vscode`, `gemini`, `opencode`.
For automation, pass a comma-separated list such as
`prism init --harness claude,codex .`; a first-time non-interactive init never assumes
all harnesses. Later runs can use `prism init --yes .` to reuse the selection recorded
in `prism.yaml`. Set the interface without a prompt with
`prism init --harness claude,codex --interface cli .` (or `--interface mcp`).
`--yes` and `--refresh` also reuse the recorded interface; a first-time
non-interactive init defaults to MCP. CLI mode requires `prism` on the agent's PATH.
Windsurf receives `AGENTS.md` steering only because its documented MCP
configuration is user-global, which Prism does not modify.
Codex loads the generated `.codex/config.toml` after the repository is trusted;
accept Codex's project-trust prompt, then restart or reload the session.

Use search to find an anchor, then ask the graph for the relationship you need:

```sh
prism search QueryData --scope text --exhaustive --files-only
prism change-impact 'QueryDataHandler.QueryData' --format text

prism query --terms ValidateRequest \
  --include graph \
  --format text

prism verify --base main --format text
```

With the MCP interface, `prism init` exposes one compact `prism` gateway with six
operations:

- `search` locates symbols and source text.
- `query` returns budgeted source around named anchors, callers, and tests.
- `read` reads a known file or range and deduplicates unchanged repeat reads.
- `lookup` returns known symbol bodies, marking any omitted content.
- `change_impact` returns indexed declarations, implementation families, and caller candidates.
- `verify` reviews a diff against indexed potential impact sites.

Run `prism mcp --legacy` only for compatibility testing with the former six-tool
surface; normal and init-generated MCP launches use the compact gateway.

CLI help is authoritative for the complete command and flag list:

```sh
prism --help
prism doctor .
```

### Read-guard hook (Claude Code)

```sh
prism init --read-guard .      # install
prism init --no-read-guard .   # uninstall
```

Installs a hook that denies a native `Read` once prism has already delivered
that exact line range earlier in the same session, pointing the agent back
at what it already has instead of paying for it twice. Off by default.
Claude Code only; hooks are a Claude Code mechanism. The v0.87.39 benchmark
above did not use it.

A delivered range stops counting as soon as its file may have changed: an
`Edit`/`Write`/`MultiEdit`/`NotebookEdit` of the file drops its ranges, a
Bash command that can rewrite files (git checkout/stash/reset/apply/...,
`patch`, `sed -i`, `mv`/`cp`, formatters, codegen, redirects into files)
drops all of them, and a file whose mtime or size differs from delivery time
is never blocked. Ranges are scoped to the Claude Code session that received
them.

`--read-guard` writes `.claude/hooks/prism_read_tracker.py` and
`.claude/hooks/prism_read_guard.py`, and adds two `PostToolUse` entries (on
`mcp__prism__prism`, and on `Edit|Write|MultiEdit|NotebookEdit|Bash`) and
one `PreToolUse` entry (on `Read`) to `.claude/settings.json`. Re-running it
upgrades an older install in place. It only ever adds entries — an existing
hook you already have on `Read` or elsewhere is never modified or replaced,
prism's just runs alongside it. Session state lives in
`.claude/prism-read-guard/`, which ignores itself in git. `--no-read-guard`
removes exactly what `--read-guard` added: the two script files, only the
settings.json entries whose command is prism's own (any other hook on the
same matcher is left in place), and the state (including the
`.prism-read-tracker.json` file older releases kept at the project root).
Both are standalone actions — running either does not require selecting
harnesses or otherwise running the rest of `init`.

## Workflow for coding agents

The steps below name MCP operations (`prism(op=...)`); the CLI commands have
the same names (`prism lookup`, `prism search`, `prism change-impact`, ...).

1. Use `lookup` for a known symbol or `search` for an unknown location; batch related names in one call.
2. Use `change_impact` when callers, overrides, contracts, or other affected sites need checking; a clearly local body-only edit need not call it. Preserve the returned set when used.
3. Use `query` for related source context around explicit anchors, or `read` for a known file or range; inspect omission markers.
4. Use exhaustive text search for wide concept removals and other completeness questions.
5. Run relevant tests. `verify` is optional in every language. Consider it for Python, unchecked JavaScript, or PHP contract changes; for TypeScript or checked JavaScript, only when affected files lack a complete typecheck. Skip it after a complete build/typecheck of affected Go, Java, Rust, C/C++, or C# targets.

`prism search --scope text` is a real repository text search. The graph adds value after location: callers, implementations, type relationships, tests, architectural edges, and completeness checks.

## Architecture and CI

Add boundary rules to `prism.yaml`:

```yaml
version: 2
harnesses: "claude,codex"
mcp_surface: "compact"
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

Prism indexes Go, TypeScript/TSX, JavaScript/JSX, Python, Java, Rust, C/C++, C#, PHP, Swift, Kotlin, Objective-C, COBOL, and JCL (via the embedded Grove engine). Semantic depth varies by language and by the native toolchain available in the repository; COBOL and JCL are structural/heuristic only, with no published accuracy oracle yet. `prism doctor` reports the full per-language indexing/resolution tier and limitations breakdown (Grove's own capability manifest) under its `languages` key, alongside index/engine status.

## Interfaces

- **CLI:** local interactive use and CI
- **MCP over stdio:** Claude Code, Cursor, VS Code, Codex, Gemini, OpenCode, and other MCP clients
- **HTTP:** `prism serve --port 8888`; see [docs/HTTP_API.md](docs/HTTP_API.md)
- **Go library:** see [docs/GO_KIT.md](docs/GO_KIT.md)

Grove is embedded in the Prism binary. Prism users do not need to install or run a separate graph service.

## Security and privacy

Prism analyzes the local working tree and stores its index locally. The default MCP transport is stdio. The optional HTTP server binds to loopback by default. See [SECURITY.md](SECURITY.md) and [THREAT_MODEL.md](THREAT_MODEL.md) for reporting and trust-boundary details.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md), and [SUPPORT.md](SUPPORT.md). Accuracy changes should include an oracle-backed regression case; public performance claims should link to reproducible evidence in [provasign/research](https://github.com/provasign/research).

## License

Apache License 2.0. See [LICENSE](LICENSE).
