# How each supported agent competes with Prism's steering

Date: 2026-10-20. Research only; no product change. Sources were read at the versions named below; quotes are verbatim from source or docs, and inference is marked as such.

## Why this was done

In this Claude Code session (auto mode), the agent used no Prism for discovery although CLAUDE.md requires it. A mid-session reminder told it to "do your work through the Bash tool… read files with cat, head, or sed -n, search with grep and find". The benchmark harness runs `--dangerously-skip-permissions` and never received that text, so every measured saving assumed agents follow project steering. This report checks, for every agent `prism init` supports, what in the agent's own instructions competes with "use prism for code search", and how much weight project instruction files get.

## Summary

| Agent | Competing instruction | Rank of Prism's steering file | Better Prism route |
|---|---|---|---|
| Claude Code | Experimental auto/bypass-mode reminder: work through Bash with cat/grep/sed (flag-gated) | CLAUDE.md is a user message; docs: conflicting instructions may be picked "arbitrarily" | CLI (it is a Bash command) plus a line reconciling the reminder |
| Codex CLI | Every current model template: "you reach first for `rg`" | AGENTS.md is a **user**-role message; base prompt is **developer** | CLI; frame prism for symbol questions; `developer_instructions` is allowed in project `.codex/config.toml` |
| OpenCode | Shell tool: "DO NOT use it for … searching, finding files"; prompts push Grep/Glob/Task | AGENTS.md in the system message, no stated precedence | MCP (`prism_prism`); explore subagent denies it unless permitted |
| Gemini CLI | Default "use grep_search and glob" | GEMINI.md "take[s] absolute precedence over … tool defaults" | MCP (`mcp_prism_prism`); steering works as a tool preference |
| VS Code Copilot | GPT-5.x prompts: "prefer `rg`", reads via `cat`/`sed`; others: semantic_search | Instructions in the system message, "additive", no precedence | MCP (`mcp_prism_prism`); its rule "If a tool exists … you MUST use that tool instead of running a terminal command" favors MCP |
| Cursor | "ALWAYS prefer using codebase_search over grep" (published prompt) | Rules "included at the start of the model context"; AGENTS.md supported, precedence undocumented | MCP; an Always-Apply `.cursor/rules` file |
| Windsurf → Devin Desktop | Built-in codebase_search/grep_search; "Only call tools when they are absolutely necessary" | Root AGENTS.md is an always-on rule in the system prompt | MCP; project MCP now exists for Devin Local |

Main conclusion (inference from the table): **the right interface depends on the agent.** Shell-first agents (Claude Code with the Bash-first reminder, Codex) fit the CLI, because `prism` is a shell command. Dedicated-tool-first agents (OpenCode, Gemini CLI, Copilot, Cursor, Windsurf) fit MCP, because their prompts tell the model to prefer specialized tools over the terminal, and OpenCode's shell tool forbids searching through it. One steering block for all agents cannot be right for both groups.

## Claude Code

Verified:
- The reminder is in the v2.1.302 binary. It is gated by GrowthBook flag `tengu_thrifty_sonic` (env override `CLAUDE_CODE_THRIFTY_SONIC`), with a second flag `tengu_cozy_teapot` / `CLAUDE_CODE_COZY_TEAPOT` choosing strict or relaxed wording ("The choice is yours"). Checked with `strings` on the local binary.
- A headless `claude -p --permission-mode auto` probe here did not receive it. GitHub issue #92271 reproduced it headless for one model and not others, so assignment is per session/model, not interactive-only.
- `{"env":{"CLAUDE_CODE_THRIFTY_SONIC":"0"}}` in settings disables it (#92271). The conflict with code-index MCP tools is reported in #94333 and #96094 (362 of 930 Bash calls were grep); no maintainer response.
- Docs (code.claude.com/docs/en/memory): "CLAUDE.md content is delivered as a user message after the system prompt"; "Claude treats them as context, not enforced configuration"; "if two instructions contradict each other, Claude may pick one arbitrarily"; "Check whether your instruction competes with guidance Claude Code adds on its own."

Measured (injected-text trial, `results/automode12-2026-10-20` and `automode12c-2026-10-20`, 12 tasks per arm, v0.87.37 CLI):

| Arm | Solved | Task cost @5m | prism | grep/rg/find | cat/head/sed |
|---|---|---|---|---|---|
| A plain | 11 | $0.473 | 17 | 15 | 16 |
| B reminder text | 10 | $0.404 | 10 | 30 | 33 |
| C reminder + reconciling line | 11 | $0.439 | 12 | 30 | 42 |

The reminder cut prism use and doubled grep. The reconciling line did not bring prism use back. Sessions with the reminder were cheaper, not more expensive.

## Codex CLI

Verified (github.com/openai/codex at feadd94; local codex-cli 0.163.0):
- `codex-rs/models-manager/models.json`, every current template (11 occurrences): "When you search for text or files, you reach first for `rg` or `rg --files`; they are much faster than alternatives like `grep`."
- AGENTS.md is injected with role `"user"` (`core/src/context/user_instructions.rs`); base instructions are developer-role. Older prompts state "Direct system/developer/user instructions (as part of a prompt) take precedence over AGENTS.md instructions."
- Combined AGENTS.md is truncated at `project_doc_max_bytes` (default 32 KiB); skipped when the project is explicitly untrusted.
- No approval or sandbox mode changes tool guidance.
- `developer_instructions` is not on `PROJECT_LOCAL_CONFIG_DENYLIST` (`config/src/loader/mod.rs`), so a project `.codex/config.toml` may set it. Prism already writes that file.
- Probe (model self-report, not behavior): asked to rank two AGENTS.md wordings against its `rg` rule, it said it would keep `rg` first: "The distinction is instruction priority, not whether the repository uses emphatic or descriptive wording."

Counter-evidence: the 2026-10-09 audit counted 594 prism calls in Codex sessions (MCP), so the self-report overstates the effect. Untested: whether `developer_instructions` changes behavior; whether `prism` CLI needing to write its index fails under `-s read-only`.

## OpenCode

Verified (v1.17.18 source; installed `/opt/homebrew/bin/opencode` 1.17.18):
- `tool/shell/shell.txt`: "IMPORTANT: This tool is for terminal operations like git, npm, docker, etc. DO NOT use it for file operations (reading, writing, editing, searching, finding files) - use the specialized tools for this instead." This applies to every model, and directly contradicts CLI prism.
- Prompt per model family (`session/system.ts`): anthropic.txt "it is CRITICAL that you use the Task tool instead of running search commands directly"; gpt.txt/codex.txt "prefer using Glob and Grep tools".
- Explore subagent permission is `"*": "deny"` with only grep/glob/list/bash/read/web allowed (`agent/agent.ts:172`), so it cannot call `prism_prism`, and its MCP instructions are filtered out. Plan mode and anthropic.txt push exploration into it.
- AGENTS.md (first of AGENTS.md / CLAUDE.md / CONTEXT.md found) goes into the single system message; no precedence wording.
- Prism's `opencode.json` shape matches the schema; the tool appears as `prism_prism`.

## Gemini CLI

Verified (gemini-cli main d00859d):
- "**Contextual Precedence:** Instructions found in `GEMINI.md` files are foundational mandates. They take absolute precedence over the general workflows and tool defaults described in this system prompt." The memory wrapper lists "tool preferences" as overridable.
- No shell-first rule; `grep_search` is "PREFERRED over standard `run_shell_command("grep ...")`".
- MCP tools are named `mcp_{server}_{tool}` → `mcp_prism_prism`. MCP outputs are wrapped as untrusted, so directives in Prism answers are ignored.
- With trusted folders enabled, an untrusted folder drops `.gemini/settings.json` MCP servers and project memory.

## VS Code Copilot

Verified (microsoft/vscode `extensions/copilot`):
- GPT-5.x prompts: "When searching for text or files, prefer using `rg` or `rg --files`"; same prompt: "If a tool exists to perform a specific task, you MUST use that tool instead of running a terminal command."
- Default/Anthropic prompts: "use semantic_search"; "NEVER try to edit a file by running terminal commands".
- For Claude models MCP tools are deferred behind tool_search ("BLOCKING REQUIREMENT").
- `chat.useAgentsMdFile` defaults to true: Copilot also loads AGENTS.md, so a project with both files gets Prism's block twice. Docs: "Do not depend on a file order or precedence rule to resolve conflicts."
- Prism's `.vscode/mcp.json` shape is valid but pins an absolute directory; `${workspaceFolder}` is supported.

## Cursor and Windsurf

Cursor (docs plus published prompts, not source): rules are "included at the start of the model context"; AGENTS.md supported, precedence undocumented; published agent prompts say "ALWAYS prefer using codebase_search over grep for searching for code".

Windsurf now redirects to Devin Desktop. Default agent "Devin Local" reads AGENTS.md/CLAUDE.md as always-on rules and supports project MCP at `.devin/mcp_config.json`; legacy Cascade puts root AGENTS.md in the system prompt and has user-level MCP only. Prism's Windsurf path (steering only, warning that no project MCP exists) is out of date.

## Proposed changes (not made; each needs its own check)

1. Claude Code: do not add the reconciling line (arm C: no effect). Do not set `CLAUDE_CODE_THRIFTY_SONIC` from `prism init`: that changes the user's harness, not Prism's answers.
2. Default `--interface` by agent: CLI for Claude Code and Codex; MCP for OpenCode, Gemini, Copilot, Cursor, Windsurf.
3. Agent-specific steering: name the tool as each agent sees it (`prism_prism`, `mcp_prism_prism`), and name the built-in it replaces (codebase_search, grep_search, Grep/Glob, rg).
4. Codex: test `developer_instructions` in the project config, and framing prism for symbol questions alongside `rg`, in a Codex CLI trial.
5. OpenCode: write `"permission": {"prism_*": "allow"}` so the explore subagent can use Prism.
6. Copilot: avoid duplicate blocks when both AGENTS.md and copilot-instructions.md are written; use `${workspaceFolder}`.
7. Windsurf: support Devin Local's `.devin/mcp_config.json`.
8. Gemini: warn in `prism init` when folder trust would drop the config.

Working files (source clones, extracted prompts) are in the session scratchpad and are not part of this report.
