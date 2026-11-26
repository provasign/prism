package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/provasign/prism/internal/core"
)

func cmdChangeImpact(args []string) int {
	for _, a := range args {
		if a == "--diff" {
			return cmdChangeImpactDiff(args)
		}
	}
	args, rc := positionalsFirst("change-impact", "a symbol", "prism change-impact Type.method [more names] [./dir | --dir <path>]", 1, args, "--dir", "--repo", "--format", "--file", "--path")
	if rc != 0 {
		return rc
	}
	queries := []string{args[0]}
	dir, dirSet := ".", false
	format := formatText
	file := ""
	repo := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dir":
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			dir, dirSet = v, true
			i++
		case "--repo":
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			repo = v
			i++
		case "--format":
			f, err := formatFlag("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--file", "--path":
			// Disambiguate same-named types in different packages: only
			// types declared in a matching file seed the closure.
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			file = v
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("change-impact", a)
			}
			if lookupArgIsPath(a) {
				if err := positionalDir("change-impact", &dir, &dirSet, a); err != nil {
					return usageError(err)
				}
			} else {
				queries = append(queries, a)
			}
		}
	}
	if len(queries) > maxBatchTerms {
		return usageError(fmt.Errorf("change-impact: at most %d names per call", maxBatchTerms))
	}
	session, err := newRepoToolSession(dir, repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("change-impact", err))
		return 1
	}
	defer session.close()
	var results []map[string]any
	var texts strings.Builder
	allRendered, failed := true, false
	var firstErr error
	for _, query := range queries {
		warnShellExpandedName("change-impact", query)
		impactReq := core.ChangeImpactRequest{Query: query, File: file}
		out, text, rendered, err := session.invoke(impactReq)
		if err != nil && repo == "" {
			// The name is not defined here but a linked repository may define
			// it (lookup already answers that way). Ask them in turn instead
			// of making the agent rerun with --repo: that rerun was 2 of 10
			// change-impact calls in the 11-16 cross-repo run.
			out, text, rendered, err = changeImpactInLinked(dir, session, impactReq, err)
		}
		if len(queries) == 1 {
			if err != nil {
				return reportToolError("change-impact", err, format)
			}
			printToolResult("change-impact", out, text, rendered, format)
			return 0
		}
		entry := map[string]any{"query": query}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			entry["error"] = err.Error()
			fmt.Fprintf(&texts, "// %s: ERROR: %v\n", query, err)
			failed = true
		} else {
			entry["result"] = out
			allRendered = allRendered && rendered
			texts.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				texts.WriteByte('\n')
			}
		}
		results = append(results, entry)
	}
	printToolResult("change-impact", map[string]any{"results": results}, texts.String(), allRendered, format)
	if failed {
		return exitForError(firstErr)
	}
	return 0
}

// changeImpactInLinked retries an indexed-name miss in the linked repository
// selected by lookup. Ambiguous definitions remain errors; linked clients are
// owned by the parent session and reused for the rest of the batch.
func changeImpactInLinked(dir string, session *repoToolSession, req core.ChangeImpactRequest, miss error) (any, string, bool, error) {
	if !strings.Contains(miss.Error(), "is indexed") || session.handler.Workspace == nil || len(session.handler.Workspace.Linked()) == 0 {
		return nil, "", false, miss
	}
	name := req.Query
	found, err := session.handler.Do(core.LookupRequest{Names: []string{name}})
	m, _ := found.(map[string]any)
	if err != nil || m == nil {
		return nil, "", false, miss
	}
	if ambiguous, _ := m["ambiguous"].(bool); ambiguous {
		return nil, "", false, fmt.Errorf("%q is ambiguous across linked definitions: %v; select a repository with --repo <id> and, if needed, a declaration with --file <path>", name, m["candidates"])
	}
	note, _ := m["note"].(string)
	const marker = "shown from linked repository "
	i := strings.Index(note, marker)
	if i < 0 {
		return nil, "", false, miss
	}
	ids := strings.Fields(note[i+len(marker):])
	if len(ids) == 0 {
		return nil, "", false, miss
	}
	id := ids[0]
	linked := session.linked[id]
	if linked == nil {
		linked, err = newRepoToolSession(dir, id)
		if err != nil {
			return nil, "", false, err
		}
		if session.linked == nil {
			session.linked = make(map[string]*repoToolSession)
		}
		session.linked[id] = linked
	}
	return linked.invoke(req)
}

// cmdChangeImpactDiff runs change-impact over the working-tree diff
// (GAP-05): what the diff reaches, where verify says whether it is
// complete.
func cmdChangeImpactDiff(args []string) int {
	dir, dirSet := ".", false
	format := formatText
	var diffReq core.ChangeImpactDiffRequest
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--diff":
		case "--base":
			v, err := flagValue("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			diffReq.Base = v
			i++
		case "--format":
			f, err := formatFlag("change-impact", args, i)
			if err != nil {
				return usageError(err)
			}
			format = f
			i++
		case "--json":
			format = formatJSON
		default:
			if strings.HasPrefix(a, "-") {
				return rejectUnknownFlag("change-impact", a)
			}
			if err := positionalDir("change-impact", &dir, &dirSet, a); err != nil {
				return usageError(err)
			}
		}
	}
	out, text, rendered, err := invokeToolText(dir, diffReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, prefixOnce("change-impact", err))
		if strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "unknown revision") {
			return exitUsage
		}
		return exitForError(err)
	}
	printToolResult("change-impact", out, text, rendered, format)
	return exitOK
}
