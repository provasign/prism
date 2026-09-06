# Scoped Lookup Model Comparison

Date: 2026-09-06. Product under test: `71a509b`; documentation HEAD `0743371`.
Run: `prism-scoped-ab-1_9slfbq`. **Both efficiency targets failed.**

Eight fresh Codex cells compared the prior Prism binary with scoped lookup on
Gin and Django, two repeats each. Model (`gpt-5.5`, medium), CLI (0.153.4), task
snapshots, exact-site scorer, native-tool access, and direct-routing guidance
were fixed. Only the binary differed. This is not a native four-way comparison.

## Results

| Metric | Before Prism | Scoped-lookup Prism |
| --- | ---: | ---: |
| Total input + output tokens | 250,078 | 273,667 |
| Fixed-rate API-equivalent cost | $0.494915 | $0.523492 |
| Mean exact-site recall / precision | 100% / 100% | 100% / 100% |
| False-complete answers | 0 | 0 |
| Tool calls, including errors | 19 | 18 |
| Post-impact searches / lookups | 3 / 5 | 5 / 3 |

The candidate used **9.4% more aggregate tokens**, **11.5% more tokens on the
median pair**, and cost **5.8% more**. Frozen targets remain at least 25% median
paired token savings and 30% aggregate cost savings, without paired recall loss.
Accuracy passed; neither efficiency criterion passed. There is no task-accuracy
gain on this already-perfect sample.

| Pair | Before tokens | After tokens | Candidate token change | Before cost | After cost |
| --- | ---: | ---: | ---: | ---: | ---: |
| Gin r1 | 73,187 | 77,834 | +6.3% | $0.135520 | $0.131012 |
| Gin r2 | 83,650 | 97,510 | +16.6% | $0.174220 | $0.154043 |
| Django r1 | 53,736 | 41,742 | -22.3% | $0.108723 | $0.085537 |
| Django r2 | 39,505 | 56,581 | +43.2% | $0.076452 | $0.152900 |

All eight attempts were valid; no retries or exclusions. The run's total
estimated cost was **$1.018407**, including one recovered unsupported-parameter
lookup error in before/Gin r1. There were no setup failures. Raw CLI usage,
cache subsets, and all errors remain in the research evidence.

## Interpretation

The capability was adopted: both Django candidate cells used one exact scoped
batch containing eight identities, with no scoped errors, misses or omissions.
Gin used no scoped objects. In Django r1, five lookups became one, reducing
tokens by 22.3%. In r2, the before agent needed only impact plus text search;
the candidate loaded eight bodies before the same search, nearly doubling cost.

This supports the hypothesis that the remaining problem is choosing sufficient
evidence and reusing it, not simply making more bodies available in one call.
It does not prove the new schema caused every regression. Four pairs on two
familiar tasks, with uncontrolled provider caching and stochastic behavior, are
not a robust causal estimate or a held-out product validation. Do not compound
these percentages with the earlier guidance or delivery studies.

The measured quality is exact site-set agreement, not executable bug-fix success.
An additional coverage concern warrants investigation: after/Gin r1 impact
returned declarations-only `closed` results, but its text search exposed
interface-call sites including `Context.Stream`. Gin's two-function bug-fix
gold does not validate that impact contract. Do not suppress these searches
merely because they follow an impact result.

## Next Work

Remain in context-sufficiency work; do not advance on a claim of broad savings.

1. Classify follow-ups by new evidence: missing body, missing call/contract,
   ambiguity, freshness, or already-delivered evidence. Call order alone is not
   proof of redundancy.
2. Add free fixtures for enumeration-only versus body-needed tasks, and for the
   observed Go interface-call/`closed` discrepancy before stronger stopping cues.
3. Make task-appropriate evidence and outstanding gaps explicit. Do not force
   full-body reads after complete enumeration, or impact scans for simple
   body-local fixes without a concrete coverage need. Preserve recovery paths.
4. Freeze a new comparison only after those checks pass. Broader native
   Sonnet/Codex controls remain necessary before product-wide savings claims.

## Evidence And Verification

[Research evidence and protocol](https://github.com/provasign/research/tree/8518b5b2692dc972069a8a8de4060620f8d1d5d8/harness/runs/prism-scoped-lookup-ab-2026-09-06)
contain every raw transcript, prepared manifest and per-cell result. The
[portable replay verifier](https://github.com/provasign/research/blob/8518b5b2692dc972069a8a8de4060620f8d1d5d8/harness/runs/prism-scoped-lookup-ab-2026-09-06/verify_results.py)
rechecks all eight raw answers, normalized site sets and usage totals without
model access or temporary binaries. All 111 artifact checksum checks and 27
offline wrapper/analyzer/verifier tests pass. The prior archive is unchanged.

Cost uses the prior study's fixed-rate API-equivalent estimator, not actual
subscription or invoice billing. Pricing caveats, including excluded
long-context premiums, are explicit in the frozen protocol. No product code,
installed binary, original working checkout or global agent config changed in
this experiment. Evidence is committed separately in research; Prism keeps
this report. Neither branch was pushed or tagged.
