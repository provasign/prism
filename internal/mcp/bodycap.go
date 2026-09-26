package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/prism/internal/grove"
)

// Delivered-body size cap.
//
// Measured (jackson pr6039, rerun #51): `lookup BeanDeserializerBase
// fields:[signature,body]` returned 82,816 chars; Claude Code rejected the
// tool result as too large and spilled it to a file, and the agent fell back
// to search + read. Other class lookups in the same beds ran 14-18 KB. A
// lookup of a type is almost always asked for its shape (fields, members,
// signatures), and a member body is one lookup or read away.
//
// The cap reuses the existing budget scale: a body over compactReadLimit
// lines (the compact read window) or twice the search full-body byte bound
// is not delivered whole. A container (class/struct/interface/...) delivers
// its header lines plus a member signature outline with line spans; any other
// symbol delivers its first lines and the exact read call for the rest.
const (
	bodyCapMaxLines      = compactReadLimit           // 240
	bodyCapMaxBytes      = 2 * searchFullBodyMaxBytes // 20,000 chars (~5k tokens)
	bodyCapHeaderLines   = 20
	bodyCapOutlineMax    = 150
	bodyCapOutlineBytes  = 12000
	bodyCapSignatureWide = 140
)

// containerKinds are the symbol kinds whose body is a list of members.
var containerKinds = map[string]bool{
	"class": true, "struct": true, "interface": true, "enum": true, "trait": true,
	"module": true, "namespace": true, "type": true, "object": true, "record": true, "impl": true,
}

// bodyOverCap reports whether a delivered body exceeds the shared cap.
func bodyOverCap(body string) bool {
	if len(body) > bodyCapMaxBytes {
		return true
	}
	return strings.Count(strings.TrimRight(body, "\n"), "\n")+1 > bodyCapMaxLines
}

// capBody bounds one symbol body. start/end are the symbol's 1-based line
// span, members the file's indexed symbols (used for a container outline;
// may be nil). It returns the lines to deliver — always a contiguous prefix
// of body, so callers that number lines from start stay truthful — and an
// explanatory note (with the outline), or ok=false when no cap applies.
func capBody(body, file, kind string, start, end int, members []grove.SymbolRecord) (head, note string, ok bool) {
	if !bodyOverCap(body) {
		return body, "", false
	}
	lines := strings.SplitAfter(body, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if end < start+total-1 {
		end = start + total - 1
	}
	var outline []grove.SymbolRecord
	if containerKinds[strings.ToLower(kind)] {
		outline = directMembers(members, start, end)
	}
	if len(outline) > 0 {
		keep := bodyCapHeaderLines
		if first := outline[0].Span.Start - start; first > 0 && first < keep {
			keep = first
		}
		head = boundedPrefix(lines, keep, bodyCapMaxBytes/4)
		shown := strings.Count(head, "\n")
		var b strings.Builder
		fmt.Fprintf(&b, "BODY CAPPED: %s %s:%d-%d is %d lines / %d chars, over the delivery cap (%d lines / %d chars). "+
			"Delivered lines %d-%d and the member outline below; get a member with op=lookup (Type.member) "+
			"or a range with op=read (file, from, to).",
			kind, file, start, end, total, len(body), bodyCapMaxLines, bodyCapMaxBytes, start, start+shown-1)
		b.WriteString("\nmembers (" + fmt.Sprint(len(outline)) + "):")
		used := 0
		for i, m := range outline {
			sig := strings.Join(strings.Fields(m.Signature), " ")
			if sig == "" {
				sig = m.Name
			}
			if len(sig) > bodyCapSignatureWide {
				sig = sig[:bodyCapSignatureWide-3] + "..."
			}
			line := fmt.Sprintf("\n  %d-%d  %s  %s", m.Span.Start, m.Span.End, m.Kind, sig)
			if i >= bodyCapOutlineMax || used+len(line) > bodyCapOutlineBytes {
				fmt.Fprintf(&b, "\n  … %d more members not listed; op=search with scope=symbols and paths=[%q] lists them", len(outline)-i, file)
				break
			}
			used += len(line)
			b.WriteString(line)
		}
		return head, b.String(), true
	}
	head = boundedPrefix(lines, bodyCapMaxLines, bodyCapMaxBytes)
	shown := strings.Count(head, "\n")
	next := start + shown
	to := minInt(end, next+compactReadLimit-1)
	note = fmt.Sprintf("BODY CAPPED: %s %s:%d-%d is %d lines / %d chars, over the delivery cap (%d lines / %d chars). "+
		"Delivered lines %d-%d; continue with op=read file=%q from=%d to=%d.",
		kind, file, start, end, total, len(body), bodyCapMaxLines, bodyCapMaxBytes, start, next-1, file, next, to)
	return head, note, true
}

// boundedPrefix joins at most maxLines leading lines within maxBytes.
func boundedPrefix(lines []string, maxLines, maxBytes int) string {
	var b strings.Builder
	for i, l := range lines {
		if i >= maxLines || (i > 0 && b.Len()+len(l) > maxBytes) {
			break
		}
		if !strings.HasSuffix(l, "\n") {
			l += "\n"
		}
		b.WriteString(l)
	}
	return b.String()
}

// directMembers returns the symbols nested directly inside [start,end]: inside
// the span, not the container itself, and not inside another such member.
func directMembers(syms []grove.SymbolRecord, start, end int) []grove.SymbolRecord {
	var inside []grove.SymbolRecord
	for _, s := range syms {
		if s.Span.Start < start || s.Span.End > end || (s.Span.Start == start && s.Span.End == end) {
			continue
		}
		if s.Kind == "file" || s.Kind == "document" {
			continue
		}
		inside = append(inside, s)
	}
	sort.SliceStable(inside, func(i, j int) bool {
		if inside[i].Span.Start != inside[j].Span.Start {
			return inside[i].Span.Start < inside[j].Span.Start
		}
		return inside[i].Span.End > inside[j].Span.End
	})
	var out []grove.SymbolRecord
	coveredTo := 0
	for _, s := range inside {
		if s.Span.Start <= coveredTo && s.Span.End <= coveredTo {
			continue // nested in the previous direct member
		}
		out = append(out, s)
		if s.Span.End > coveredTo {
			coveredTo = s.Span.End
		}
	}
	return out
}

// capLookupResult applies the shared body cap to one lookup result in place:
// the default {symbol, content} shape and the fields=[body] projection shape.
// Overload bodies are already bounded by the lookup body budget.
func (h *Handler) capLookupResult(ctx context.Context, result any) any {
	out, ok := result.(map[string]any)
	if !ok {
		return result
	}
	members := func(file string) []grove.SymbolRecord {
		if h.Grove == nil || file == "" {
			return nil
		}
		syms, err := h.Grove.FileSymbols(ctx, file)
		if err != nil {
			return nil
		}
		return syms
	}
	if content, _ := out["content"].(string); content != "" && bodyOverCap(content) {
		var file, kind string
		var start, end int
		switch sym := out["symbol"].(type) {
		case grove.SymbolRecord:
			file, kind, start, end = sym.FilePath, sym.Kind, sym.Span.Start, sym.Span.End
		case *grove.SymbolRecord:
			if sym != nil {
				file, kind, start, end = sym.FilePath, sym.Kind, sym.Span.Start, sym.Span.End
			}
		case map[string]any:
			file, _ = sym["filePath"].(string)
			kind, _ = sym["kind"].(string)
			span, _ := sym["span"].(map[string]any)
			start, end = intArg(span, "start", 0), intArg(span, "end", 0)
		}
		if start > 0 {
			if head, note, capped := capBody(content, file, kind, start, end, members(file)); capped {
				out["content"] = head
				// The symbol record carries the same body as RawText; the
				// JSON form and the batch byte budget would still pay for it.
				switch sym := out["symbol"].(type) {
				case grove.SymbolRecord:
					sym.RawText = head
					sym.CallSites = nil
					out["symbol"] = sym
				case *grove.SymbolRecord:
					trimmed := *sym
					trimmed.RawText = head
					trimmed.CallSites = nil
					out["symbol"] = trimmed
				case map[string]any:
					trimmed := make(map[string]any, len(sym))
					for k, v := range sym {
						trimmed[k] = v
					}
					trimmed["rawText"] = head
					delete(trimmed, "callSites")
					out["symbol"] = trimmed
				}
				out["note"] = appendNote(stringArg(out, "note", ""), strings.ReplaceAll(note, "\n", "\n// "))
			}
		}
	}
	for _, key := range []string{"body", "source"} {
		body, _ := out[key].(string)
		if body == "" || !bodyOverCap(body) {
			continue
		}
		file, _ := out["file"].(string)
		start := intArg(out, "line", 0)
		kind, _ := out["kind"].(string)
		end := 0
		syms := members(file)
		for _, s := range syms {
			if s.Span.Start == start && (kind == "" || s.Kind == kind) {
				if s.Span.End > end {
					end, kind = s.Span.End, s.Kind
				}
			}
		}
		if start <= 0 {
			start = 1
		}
		if head, note, capped := capBody(body, file, kind, start, end, syms); capped {
			out[key] = head + "// " + strings.ReplaceAll(note, "\n", "\n// ")
		}
	}
	return out
}
