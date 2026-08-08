package mcp

// Graph-delivery deduplication: the session-compression contract, extended
// from file reads to the whole-repo graph tools (change_impact, map,
// dead_code, …), whose payloads are the fattest Prism sends (a 310-site
// change_impact ≈ 26k tokens).
//
// Design rule — dedupe the DELIVERY, never the computation. A graph result
// depends on many files, so no input hash can prove an old delivery is
// still valid. Instead every call recomputes (the traversal is milliseconds;
// the tokens are the cost), canonicalizes, and hashes the RESULT. Only when
// the fresh result is byte-identical to what this session already received
// does Prism send a one-line [prism:cached] pointer instead of the body.
// Freshness is proven by recomputation; the hash only decides rendering.
// A changed result — even by one site — is always delivered in full:
// complete-set tools deliver complete sets, never deltas an agent must
// mentally merge (the relay rule exists because re-assembled sets drop
// sites).
//
// Attention discipline mirrors the compressor: the first identical repeat
// gets a pointer unconditionally (just delivered); later repeats get one
// only at High confidence that the prior delivery is still inside the
// agent's attention window — an agent asking a third time has probably
// lost it, so Prism re-sends rather than pointing at content the model can
// no longer see.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/provasign/prism/internal/compression"
	"github.com/provasign/prism/internal/ranking"
	"github.com/provasign/prism/internal/session"
)

// The tools wired through this dedupe (see Invoke): change_impact,
// missing_implementations, map, rename_plan, dead_code, cycles.
// prism_verify and prism_arch_check are deliberately EXCLUDED: they are
// gates, and a gate's verdict is always delivered in full.

// minGraphPointerTokens: below this, the pointer line plus any ambiguity it
// introduces costs more than re-sending the result. Small results are never
// pointered.
const minGraphPointerTokens = 600

// refreshIndexBestEffort delta-reindexes before a whole-repo graph tool
// runs. Best-effort: an index failure must not fail the tool — the result
// is then computed against the existing (possibly stale) index, which is
// exactly today's behavior without the refresh.
func (h *Handler) refreshIndexBestEffort(ctx context.Context) {
	if h.Grove == nil {
		return
	}
	_, _ = h.Grove.Index(ctx, h.Root)
}

// graphDelivery adapts graphDedupe to Invoke's (result, error) dispatch
// shape: errors pass through untouched; successful results go through the
// delivery dedupe.
func (h *Handler) graphDelivery(name string, args map[string]any) func(any, error) (any, error) {
	return func(out any, err error) (any, error) {
		if err != nil {
			return out, err
		}
		return h.graphDedupe(name, args, out), nil
	}
}

// graphDedupe decides how to deliver a graph tool's freshly computed result.
// It returns the original result, or a compact cached-pointer response when
// the identical payload was already delivered this session and is still
// confidently within the agent's context. Never fails: any marshalling
// surprise delivers the full result.
func (h *Handler) graphDedupe(name string, args map[string]any, out any) any {
	if h.Session == nil || out == nil {
		return out
	}
	payload, err := json.Marshal(out) // map keys sort: canonical for equal values
	if err != nil {
		return out
	}
	tokens := ranking.EstimateTokens(string(payload))
	if tokens < minGraphPointerTokens {
		return out
	}
	key := graphDeliveryKey(name, args)
	hash := compression.Hash(string(payload))
	contextUsed := int64(intArg(args, "context_used", 0))

	entry, seen, sameHash := h.Session.Lookup(key, hash)
	if seen && sameHash {
		window := h.Cfg.WithModel(stringArg(args, "model", "")).ContextWindow()
		conf := h.confidenceFor(entry, contextUsed, window)
		// 2nd delivery: always a pointer (the body just went out). 3rd+:
		// only while demonstrably within the attention window — repeated
		// re-asking is itself evidence the prior delivery scrolled out.
		if entry.AccessCount == 1 || conf == session.High {
			// Capture the delivery ordinal BEFORE Record: Lookup returns the
			// live entry, so Record's increment would otherwise double-count.
			deliveries := entry.AccessCount + 1
			h.Session.Record(key, hash, int64(tokens), "sha-pointer")
			h.Session.RecordContextUsed(key, contextUsed)
			pointer := graphPointerResponse(name, hash, deliveries, out)
			if pj, err := json.Marshal(pointer); err == nil {
				h.Ledger.Record(name, tokens, ranking.EstimateTokens(string(pj)))
			}
			return pointer
		}
	}
	// Fresh result, changed result, or low-confidence repeat: full delivery.
	h.Session.Record(key, hash, int64(tokens), "full")
	h.Session.RecordContextUsed(key, contextUsed)
	return out
}

// graphDeliveryKey identifies one logical query in the session tracker.
// Args that vary per call without changing the answer (context_used, model)
// are excluded; everything else (query, depth, roots, …) is part of the
// identity. The "graph://" scheme keeps these entries from ever colliding
// with real file paths in the shared LRU.
func graphDeliveryKey(name string, args map[string]any) string {
	filtered := make(map[string]any, len(args))
	for k, v := range args {
		switch k {
		case "context_used", "model", "dir":
			continue
		}
		filtered[k] = v
	}
	id, err := json.Marshal(filtered) // sorted keys: stable across calls
	if err != nil {
		id = []byte("{}")
	}
	return "graph://" + name + "?" + string(id)
}

// graphPointerResponse is the cached-delivery rendering: the compressor's
// [prism:cached] contract plus a structural summary (top-level list counts
// and identity scalars) so the agent can sanity-check WHAT it already has
// without Prism re-sending a single site.
func graphPointerResponse(name, hash string, seenCount int, out any) map[string]any {
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}
	resp := map[string]any{
		"cached": true,
		"tool":   name,
		"sha":    short,
		"seen":   seenCount,
		"note": fmt.Sprintf("// [prism:cached] %s @sha:%s (seen ×%d, result recomputed just now and is "+
			"IDENTICAL — prior delivery still in context). This is NOT an error or an empty result: "+
			"use the copy delivered earlier this session.", name, short, seenCount),
	}
	// Summarize without re-sending: array lengths + identity strings.
	var m map[string]any
	if b, err := json.Marshal(out); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if len(m) > 0 {
		summary := map[string]any{}
		for k, v := range m {
			switch tv := v.(type) {
			case []any:
				summary[k] = len(tv)
			case string:
				switch k {
				case "query", "completeness", "newName", "scope":
					summary[k] = tv
				}
			}
		}
		if len(summary) > 0 {
			resp["summary"] = summary
		}
	}
	return resp
}
