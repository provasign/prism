# CLI vs resident MCP latency on four corpus repos (2026-10-18, no model calls)

`lat.py` copies each repo, indexes it with one binary, and times 5 ops x 5 reps:
CLI as a fresh process per call on a warm index, MCP as one resident compact server.
Medians in `lat_results.json`. The CLI penalty is the per-call index load: ~40 ms
(click, 165 files), 150-230 ms (pydantic, gh cli), 0.7-0.9 s (django, 368 MB index).
`read` is 11-15 ms everywhere. Warm page cache, shared machine.
