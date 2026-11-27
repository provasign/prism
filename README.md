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
(2026-10-21), measured on v0.87.39; v1.0.0 differs from it only in CLI help text. Claude Code with Sonnet 5.5, native tools vs Prism through the CLI
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
brew install provasign/tap/prism

# macOS or Linux installer, pinned to a release
curl -fsSL https://raw.githubusercontent.com/provasign/prism/v1.3.5/install.sh | VERSION=v1.3.5 bash

# Windows PowerShell
$env:VERSION="v1.3.5"; irm https://raw.githubusercontent.com/provasign/prism/v1.3.5/install.ps1 | iex
```

The installer verifies the SHA-256 checksum, and the release's signature or
build attestation when `cosign` or a logged-in `gh` is installed
(`PRISM_REQUIRE_PROVENANCE=1` makes that required). It does not edit your
shell rc file unless you pass `--modify-path` (`… | bash -s -- --modify-path`),
and `--dry-run` lists what it would write.

The installer writes to `~/bin` by default. Set `INSTALL_DIR` to choose another directory.
Use either Homebrew or the standalone installer as the authoritative installation. If both
are present with different versions, `prism init` and the MCP server report their paths.
The standalone installer verifies the installed version and removes Prism registrations left
by the old global configuration model. Running Prism MCP servers keep working through an
upgrade: each switches to the new binary on its next request (`--stop-servers` stops them
instead). After changing installation methods, run `prism cleanup-global` and `prism init` in
each project.

The current release is v1.3.5. It embeds Grove v0.64.5 (Astkit v0.16.10).
Upgrading from v1.2: see [Upgrading to v1.3](#upgrading-to-v13).

Build from source with `make build`; run the full test suite with `make test`.

## Quick start

From the root of a repository:

```sh
prism init .
```

`prism init --dry-run .` prints a diff of every file it would write and writes nothing.
`prism uninstall .` later removes exactly what init added (recorded in
`.grove/install-manifest.json`) and leaves every other byte of those files alone.

This detects installed and previously configured harnesses, recommends those as the
default, and asks which to configure. It then asks how agents should reach Prism:
**CLI** (the default, and the configuration the reference benchmark measured) registers
no MCP server and steers agents to run the `prism` command in their shell; **MCP**
registers the compact project-local server. Both
choices write `prism.yaml` and current instruction files. CLI init removes Prism's
own MCP registration from the selected harnesses' project configs if present.
The interactive prompt gives a source-file-size hint: CLI suits occasional calls,
while a resident MCP server is faster for repeated discovery, especially in a
large repository. It does not change the default or claim a model-cost saving.
A project that already has a `prism.yaml` without a recorded interface was set up when
MCP was the default; init keeps it on MCP unless you pass `--interface cli`.
Harness ids: `claude`, `codex`, `cursor`, `windsurf`, `vscode`, `gemini`, `opencode`.
For automation, pass a comma-separated list such as
`prism init --harness claude,codex .`; a first-time non-interactive init never assumes
all harnesses. Later runs can use `prism init --yes .` to reuse the selection recorded
in `prism.yaml`. Set the interface without a prompt with
`prism init --harness claude,codex --interface cli .` (or `--interface mcp`).
`--yes` and `--refresh` also reuse the recorded interface; a first-time
non-interactive init defaults to CLI. CLI mode requires `prism` on the agent's PATH.
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

With the MCP interface, `prism init` exposes one compact `prism` gateway with seven
operations:

- `search` locates symbols and source text.
- `query` returns budgeted source around named anchors, callers, and tests.
- `read` reads a known file or range and deduplicates unchanged repeat reads.
- `lookup` returns known symbol bodies, marking any omitted content.
- `change_impact` returns indexed declarations, implementation families, and caller candidates.
- `verify` reviews a diff against indexed potential impact sites.
- `missing_implementations` lists the types that do not yet implement an interface member,
  including one you are about to add.

Run `prism mcp --legacy` only for compatibility testing with the former six-tool
surface; normal and init-generated MCP launches use the compact gateway.

More graph commands on the CLI:

```sh
prism references Config.Model --kind write   # resolved sites, tagged decl/call/read/write/type-use
prism callers Handler.toolVerify --depth 3   # transitive callers as a tree
prism path cmdCI Handler.toolVerify          # shortest resolved call path
prism lookup internal/config/config.go:16    # a path:line names the symbol declared there
prism test-plan --format targets             # runner targets: Go -run patterns, pytest ids, Class#method
prism change-impact --diff --base main       # what the working-tree diff reaches
```

Every command prints text by default; pass `--format json` for machine-readable output.
Exit codes are the same everywhere: 0 answered, 1 finding, 2 usage error, 3 degraded,
4 not found, 5 ambiguous, 70 internal failure (`prism help exit-codes`).

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
arch_deny: internal/cli -> internal/mcp    # one rule per line; repeat the key
arch_deny: internal/config -> internal/*
```

`prism config .` lists every key with its value and where it came from; `prism config --check .`
exits 1 on an unknown key or an invalid value.

Then run:

```sh
prism map .
prism arch .
prism verify . --base main
```

`prism arch` reports the concrete `file:line` evidence for each forbidden component edge. `prism verify` checks the current diff for missed impact sites, new component dependencies, and introduced architecture violations.

As a pull-request gate, `prism ci . --base origin/main --format github` fails on a missed site or a new architecture violation and annotates the lines; the composite action in [`.github/actions/prism-ci`](.github/actions/prism-ci/action.yml) runs it in GitHub Actions and can post one sticky comment (`ci_fail_on:` and `ci_comment:` in `prism.yaml`).

## Working across repositories

Link the other repositories a change can reach, and the same commands answer for them:

```sh
cd library
prism workspace add ../app             # a local checkout; or a git URL[@ref], fetched read-only
prism change-impact Client.Send        # call sites in app first, then this repository
prism lookup app.Handler --repo app    # answer from one linked repository
prism workspace discover               # related checkouts found in go.mod, package.json, pom.xml, ...
```

Each repository keeps its own index; links are followed when a question is asked. `lookup`, `read`, `search`, `change-impact`, `verify` and `test-plan` cover linked repositories, and every answer says which repositories it covered and how (compiler-resolved, or name-matched only). A link written in a cloned `prism.yaml` takes effect only after you confirm it on your machine, and an untrusted repository links nothing. Consumers that are not checked out can be covered through a link server their CI uploads to. The guide: [docs/MULTI_REPO.md](docs/MULTI_REPO.md).

## Graph explorer

```sh
prism graph            # opens http://127.0.0.1:8787/ in the browser
prism graph --no-open --port 9000 path/to/repo
```

`prism graph` serves an interactive view of the indexed graph on localhost until you press Ctrl+C. It opens on the repository's packages and the dependencies between them; double-click to drill down from a directory to its files, from a file to its symbols, and from a symbol to its callers, callees and members. Click a link to see the call and type-use sites behind it. Search (`⌘K` or `/`) finds symbols, files and directories across the whole index. **Change impact** (the `i` key, or Shift+Enter in search) lays a symbol's `prism change-impact` answer onto the graph: the declaration at the centre, the overrides and implementations that change with it, every edit site with its call line, and optionally the callers one or two hops further out (reached, not edit sites). It shows whether the answer is complete for the repository and can copy the site list; changes with more than 60 edit-site declarations are drawn one node per file. Test files are left out unless you turn on **Tests** (or pass `--tests`), and **Reload** re-indexes changed files. The page is self-contained: it loads nothing from the network, and the server answers loopback requests only.

## Language support

Prism indexes Go, TypeScript/TSX, JavaScript/JSX, Python, Java, Rust, C/C++, C#, PHP, Swift, Kotlin, Objective-C, COBOL, and JCL (via the embedded Grove engine). Semantic depth varies by language and by the native toolchain available in the repository; COBOL and JCL are structural/heuristic only, with no published accuracy oracle yet. `prism doctor` reports the full per-language indexing/resolution tier and limitations breakdown (Grove's own capability manifest) under its `languages` key, alongside index/engine status.

## Interfaces

- **CLI:** local interactive use and CI
- **MCP over stdio:** Claude Code, Cursor, VS Code, Codex, Gemini, OpenCode, and other MCP clients
- **Browser:** `prism graph` — interactive graph explorer on localhost
- **HTTP:** `prism serve --port 8888`; see [docs/HTTP_API.md](docs/HTTP_API.md)
- **Go library:** see [docs/GO_KIT.md](docs/GO_KIT.md)

Grove is embedded in the Prism binary. Prism users do not need to install or run a separate graph service.

## Security and privacy

Prism analyzes the local working tree and stores its index locally. The default MCP transport is stdio. The optional HTTP server and `prism graph` bind to loopback and require a per-run token.

Indexing Java (Maven/Gradle), TypeScript and Rust runs the repository's own build tools. Prism prints a one-time notice when it does; for a repository you do not trust, run `prism untrust .` (or set `PRISM_UNTRUSTED=1`): its build tools never run, analysis for those languages is name-based, and it links no other repository. `prism trust .` records the opposite and silences the notice. Text search and `read` withhold the contents of `.env`, key and credential files unless you pass `--include-sensitive`; `sensitive_paths:` in `prism.yaml` adds patterns. See [SECURITY.md](SECURITY.md) and [THREAT_MODEL.md](THREAT_MODEL.md) for reporting and trust-boundary details.

## Upgrading to v1.3

v1.3 keeps every answer a v1.2 index gives (verified on a v1.2-initialised repository: same change-impact sites, same lookups, the same MCP session from the v1.2 `.mcp.json`). What changes for scripts and setups:

- **Text is the default output of every command.** A script that parsed JSON from a command whose default was JSON must pass `--format json`.
- **Exit codes are one table.** A name that does not exist now exits 4 (it used to exit 0 on some commands), an ambiguous name 5, a failure 70.
- **First index prints a trust notice** when the repository's build tools will run; `prism trust .` silences it.
- **A query in a directory that is no project** (no `.git`, `prism.yaml`, `.grove` or build manifest) exits 2 instead of indexing it; `prism index <dir>` still builds there.
- **Committed MCP configs name plain `prism`.** `prism doctor` flags a config that runs prism by an absolute path; `prism init --refresh` rewrites it.
- **`prism.yaml` typos are reported** with the nearest known key.
- **Secret files are withheld** from search and read (`--include-sensitive` shows them).

The index is rebuilt once, automatically, on the first command after the upgrade.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md), and [SUPPORT.md](SUPPORT.md). Accuracy changes should include an oracle-backed regression case; public performance claims should link to reproducible evidence in [provasign/research](https://github.com/provasign/research).

## License

Apache License 2.0. See [LICENSE](LICENSE).
