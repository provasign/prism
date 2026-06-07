# Prism A/B Agent Benchmark — Payflow Project
**Date:** 2026-06-07  
**Prism version:** v0.5.5  
**Model:** claude-sonnet-4-6

---

## Purpose

Run 8 controlled subagents — 4 baseline (Read + Bash only) and 4 Prism
(prism\_query / prism\_read / prism\_search only) — on identical coding tasks
against a fresh synthetic Go project. Measure:

- Code context tokens delivered
- Total agent context consumption (subagent\_tokens)
- Tool call count
- Correctness against pre-established ground truth

No Prism source was modified for this test.

---

## Test Project: payflow

A realistic 16-file Go payment service at `/tmp/prism-ab-bench/payflow`.

```
github.com/example/payflow
├── cmd/server/main.go
├── internal/
│   ├── api/
│   │   ├── handler.go       (CreatePayment, GetPayment, ListPayments,
│   │   │                     RefundPayment, CompletePayment)
│   │   ├── middleware.go     (RequireAuth, RequireScope, TokenFromContext)
│   │   ├── router.go         (Register — wires routes + scopes)
│   │   ├── handler_test.go   (empty — deliberate gap)
│   │   └── middleware_test.go (3 tests; RequireScope NOT tested)
│   ├── auth/
│   │   ├── service.go        (ValidateToken, IssueToken, RevokeToken, RequireScope)
│   │   ├── service_test.go   (4 tests; RequireScope NOT tested)
│   │   └── token_store.go    (Save, Find, Delete, Len)
│   ├── model/
│   │   ├── payment.go        (Payment struct, PaymentStatus constants, Currency constants)
│   │   └── token.go          (Token struct, IsExpired, HasScope)
│   ├── payment/
│   │   ├── service.go        (ProcessPayment, CompletePayment, RefundPayment,
│   │   │                     GetPayment, ListPayments)
│   │   ├── service_test.go   (5 tests; CompletePayment, ValidateCurrency,
│   │   │                     ListPayments NOT tested)
│   │   └── validator.go      (ValidateAmount, ValidateCurrency, ValidatePayment)
│   └── storage/
│       ├── memory.go         (MemoryStore — SavePayment, GetPayment, ListPayments,
│       │                     UpdatePayment, DeletePayment, ConflictError)
│       ├── memory_test.go    (5 tests; UpdatePayment NOT tested)
│       └── store.go          (Store interface, NotFoundError)
```

### Ground truth — deliberate coverage gaps

| Symbol | Package | Has test? |
|--------|---------|-----------|
| `(*MemoryStore).UpdatePayment` | storage | ✗ |
| `(*Service).CompletePayment` | payment | ✗ |
| `ValidateCurrency` | payment | ✗ |
| `(*Service).ListPayments` | payment | ✗ |
| `(*auth.Service).RequireScope` | auth | ✗ |
| `(*Middleware).RequireScope` | api | ✗ |
| All `Handler.*` methods | api | ✗ |
| `(*Service).RevokeToken` | auth | ✓ (TestRevokeToken) |
| `(*Service).RefundPayment` | payment | ✓ (TestRefundPayment) |

---

## Tasks

| ID | Task | Type |
|----|------|------|
| T1 | Trace the full call chain for `CreatePayment`. List every function called, the file/line it lives in, and which tests exercise the chain. | Graph traversal |
| T2 | Enumerate every file that must change to add a new `StatusCancelled` payment status. Explain why each needs changing. | Change impact |
| T3 | List every exported function/method in this codebase that is **not** covered by any test. | Coverage audit |
| T4 | Find every call site of `auth.ValidateToken` across the codebase. | Symbol search |

---

## Results

### Code context tokens

*Baseline: bytes read / 4. Prism: `budgetUsed` field from prism\_query responses.*

| Task | Baseline (bytes→tok) | Prism budgetUsed | Code savings |
|------|---------------------|-----------------|-------------|
| T1   | 12,809 bytes → 3,202 tok | 2,824 tok | **12 %** |
| T2   | 9,679 bytes → 2,420 tok  | 1,787 tok | **26 %** |
| T3   | 22,452 bytes → 5,613 tok | 1,181 tok | **79 %** |
| T4   | 3,568 bytes → 892 tok    | 596 tok   | **33 %** |

### Total subagent tokens

*Full conversation cost — system prompt + all tool I/O + reasoning. This is
what billing sees.*

| Task | Baseline | Prism | Overhead |
|------|----------|-------|---------|
| T1   | 21,755   | 27,574 | +27 % |
| T2   | 19,497   | 35,643 | +83 % |
| T3   | 26,356   | 65,059 | +147 % |
| T4   | 15,817   | 26,476 | +67 % |

Baseline wins on total tokens in every task.

### Tool calls

| Task | Baseline | Prism | Note |
|------|----------|-------|------|
| T1   | 6        | 8     | Prism had 2 failed prism\_read (path errors) |
| T2   | 7        | 12    | Prism had 4 failed prism\_read (path errors) |
| T3   | 8        | 8     | Equal; Prism used 4 prism\_search + 3 prism\_query |
| T4   | 6        | 2     | **Prism 3× fewer — clearest Prism win** |

### Correctness

| Task | Baseline | Prism | Notes |
|------|----------|-------|-------|
| T1   | ✓ Full chain + 3 tests | ✓ Full chain + 3 tests | Identical quality |
| T2   | ✓ 5 files, correct rationale | ✓ 5 files, correct rationale | Identical answer |
| T3   | ✓ All real gaps found | ✗ 2 false positives | Prism agent claimed `RevokeToken` and `RefundPayment` untested — both are tested |
| T4   | ✓ All 4 call sites | ✓ All 4 call sites | Identical answer |

---

## Task breakdown

### T1 — Trace CreatePayment call chain

**Correct chain:**
```
Handler.CreatePayment (api/handler.go:22)
  → payment.(*Service).ProcessPayment (payment/service.go:24)
      → validator.ValidatePayment (payment/validator.go:26)
          → ValidateAmount (payment/validator.go:5)
          → ValidateCurrency (payment/validator.go:14)
      → store.SavePayment (storage/memory.go:22)
Tests: TestProcessPayment_Valid, TestProcessPayment_InvalidAmount,
       TestProcessPayment_MissingUserID
```

Both agents returned complete, correct answers. Baseline read 9 files
including some not strictly necessary (token.go, model/payment.go). Prism
delivered a smaller slice but covered the same chain.

**Winner: Draw.** Correctness identical; code context slightly favors Prism
(12% less), but total agent cost slightly favors baseline (21,755 vs 27,574).

---

### T2 — Files changed for StatusCancelled

**Correct answer (5 files):**
1. `internal/model/payment.go` — add constant
2. `internal/payment/validator.go` — accept in ValidateCurrency/ValidatePayment
3. `internal/payment/service.go` — add CancelPayment method
4. `internal/api/handler.go` — add CancelPayment handler
5. `internal/api/router.go` — register new route

Both agents returned identical, correct answers. Prism had 4 failed
prism\_read calls due to a path-resolution bug (see Findings).

**Winner: Draw on correctness.** Baseline more reliable tool execution.
Prism subagent tokens nearly 2× higher (35,643 vs 19,497).

---

### T3 — Untested exported functions

**Correct gaps (intentionally designed in):**

| Symbol | Result |
|--------|--------|
| `(*MemoryStore).UpdatePayment` | ✓ Both found |
| `(*Service).CompletePayment` | ✓ Both found |
| `ValidateCurrency` | ✓ Both found |
| `(*Service).ListPayments` | ✓ Both found |
| `(*auth.Service).RequireScope` | ✓ Both found |
| `(*Middleware).RequireScope` | ✓ Both found |
| All `Handler.*` methods | ✓ Baseline found; Prism agent noted handler gap |

**False positives:**

| Symbol | Baseline | Prism |
|--------|----------|-------|
| `(*Service).RevokeToken` | — (correctly skipped) | ✗ reported as untested |
| `(*Service).RefundPayment` | — (correctly skipped) | ✗ reported as untested |

Root cause: the Prism agent ran `prism_search` for symbol names after getting
coverage\_gaps output, then manually cross-referenced — introducing reasoning
errors. The coverage\_gaps output from prism\_query should have been trusted
directly. Baseline read every file sequentially and cross-referenced test names
against function names mechanically, avoiding this failure mode.

This task also produced the largest total-token divergence: Prism 65,059 vs
baseline 26,356. The Prism agent spent many tokens reasoning about graph data
it partially misread.

**Winner: Baseline.** Two correctness failures and 147% more total tokens.

---

### T4 — Callers of auth.ValidateToken

**Correct call sites (4):**

| Location | Caller |
|----------|--------|
| `internal/auth/service_test.go:29` | TestValidateToken\_Valid |
| `internal/auth/service_test.go:39` | TestValidateToken\_NotFound |
| `internal/auth/service_test.go:46` | TestValidateToken\_Expired |
| `internal/api/middleware.go:28` | RequireAuth |

Baseline: 6 tool calls (4 Bash greps + 2 file reads).  
Prism: 2 tool calls (1 prism\_search + 1 prism\_query). Both returned all 4 sites.

**Winner: Prism.** Same correctness, 3× fewer tool calls, 33% less code content,
though still higher total tokens (26,476 vs 15,817) due to JSON metadata overhead.

---

## Key findings

### F1: budgetUsed ≠ actual token cost

Prism's `budgetUsed` measures code content delivered — the text of symbols
and snippets, estimated in tokens. It does **not** include the JSON
response wrapper (chunk metadata, scores, spans, filePaths). The actual
in-context cost of a prism\_query response is higher than budgetUsed by a
metadata multiplier that grows with the number of chunks returned.

Result: Prism delivered 12–79% less code content in every task, but consumed
27–147% more total tokens in every task. At single-turn scale with a
fresh-indexed small project, baseline wins on total cost.

### F2: prism_read path resolution bug

On macOS, `/tmp` resolves to `/private/tmp`, but paths returned by Prism
use the indexed root as given. When the Prism session was started with
a path under `/tmp/...`, prism\_read calls using the canonical path
resolved by macOS failed consistently. The baseline `Read` tool has no
such issue (it always takes the absolute path the agent provides).

This caused 2–4 extra failed tool calls per Prism agent in T1 and T2,
raising subagent\_tokens and reducing reliability.

### F3: coverage_gaps needs agent discipline

T3 demonstrates that `include=["coverage_gaps"]` produces structured,
accurate data — but only if the agent trusts and uses it directly. When
the Prism agent augmented coverage\_gaps results with manual
prism\_search+reasoning, it introduced 2 false positives and consumed
2.5× the tokens of the baseline agent that simply read all files.

Takeaway: prism\_query with coverage\_gaps should be the terminal step,
not the start of a manual cross-reference chain.

### F4: Prism's clearest win is symbol blast-radius (T4)

When the task is "find all call sites of X," Prism does in 2 tool calls
what baseline does in 6. This is the canonical Prism use case: the call
graph already contains what grep would find, delivered in one structured
response. The savings grow with project size.

### F5: At session scale, the picture changes

These measurements are single-turn: each agent starts cold, indexes once,
and answers one question. Prism's compression model is designed for
session-scale use: the second read of a file costs ~10 tokens (SHA pointer)
instead of the full file cost. In a 20-query session across a large
codebase, where many files are read multiple times, Prism's cumulative
code-content savings compound while baseline re-reads full files each time.
This benchmark does not capture that dimension.

---

## Summary table

| Metric | T1 | T2 | T3 | T4 |
|--------|----|----|----|-----|
| Code content savings | 12% Prism | 26% Prism | 79% Prism | 33% Prism |
| Total tokens (winner) | Baseline | Baseline | Baseline | Baseline |
| Tool calls (winner) | Baseline | Baseline | Draw | **Prism** |
| Correctness (winner) | Draw | Draw | **Baseline** | Draw |

---

## Methodology notes

**Baseline agent instructions:** "Use only Read and Bash tools (grep/find).
Do not use prism\_\* tools."

**Prism agent instructions:** "Use only prism\_query, prism\_read,
prism\_search, and prism\_index. Do not use Read or Bash for file access."

**subagent\_tokens** is the token count returned by the Agent tool on
completion. It reflects the full context the model processed: system
prompt, all tool calls (request + response), and all reasoning turns.
It is the honest billing-equivalent measure and includes overhead from
both the framework and from Prism's JSON response format.

**Code content bytes** for baseline is the sum of bytes across all
`Read` and `cat` tool calls made. Divided by 4 gives an estimated
token count for the code content alone (excluding tool schemas and
reasoning). This is the directly comparable metric to Prism's
`budgetUsed`.

**Ground truth** was established by reading every source file and
test file before the agents ran, with deliberate coverage gaps
encoded in source comments (`// NOTE: X is NOT tested here.`).

---

## When to use Prism

| Scenario | Recommendation |
|----------|---------------|
| Blast-radius / call-site search on medium-to-large project | Prism first |
| Coverage-gap audit | prism\_query + coverage\_gaps, trust the output |
| Multi-turn session where same files appear in many queries | Prism — compression compounds |
| Single-turn read of a handful of small files | Baseline may be cheaper overall |
| Graph-traversal for refactor planning | Prism, especially graph\_depth=3+ |

The budget wins on code content are real and widen with project size. The
current single-agent, single-turn measurement understates Prism's value.
The subagent\_token overhead is also real and is partly a fixable bug
(prism\_read path resolution) and partly JSON metadata overhead that
could be reduced with a leaner response format.
