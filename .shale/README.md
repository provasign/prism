# .shale/ — agent session evidence

These files are written by [Shale](https://github.com/provasign/shale): one
schema-versioned YAML per AI-agent session (intent, files touched, commands
run, token cost) plus redacted prompts-only transcripts. They are committed
so the evidence travels with the code and renders as a card on the PR.

- Finalized files are append-only; edits after capture are flagged on the card.
- `local/` is gitignored working state — never commit it.
