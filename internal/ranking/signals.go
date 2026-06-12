package ranking

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/grove"
)

// SignalComputer computes individual signal values for a symbol relative to
// a task and a workspace root.
type SignalComputer struct {
	WorkspaceRoot string
	Embeddings    SemanticBackend // optional; if nil, similarity is 0

	gitMu     sync.Mutex
	gitLoaded bool
	recent    map[string]gitFileStats // per-file stats from the batched 90-day pass
	gitCache  map[string]gitFileStats // resolved per-path stats (incl. out-of-window fallback)
}

// SemanticBackend computes similarity between a task and a symbol's
// descriptive text. Grove's semantic index (via the MCP handler's adapter)
// satisfies this.
type SemanticBackend interface {
	Similarity(task string, sym grove.SymbolRecord) float64
}

type gitFileStats struct {
	LastEditDays   int
	CommitCount90d int
}

// NewSignalComputer constructs a computer rooted at workspaceRoot.
func NewSignalComputer(workspaceRoot string, embeddings SemanticBackend) *SignalComputer {
	return &SignalComputer{
		WorkspaceRoot: workspaceRoot,
		Embeddings:    embeddings,
		gitCache:      make(map[string]gitFileStats),
	}
}

// Compute returns the SignalValues for one symbol given seed symbol IDs and
// the BFS distance from any seed (0 means seed itself, math.MaxInt = unreachable).
func (c *SignalComputer) Compute(ctx context.Context, task string, sym grove.SymbolRecord, bfsDistance int, hasTestEdgeToSeed bool, sameFileAsTest bool) SignalValues {
	gv := SignalValues{}

	// Signal 1 — Graph distance
	if bfsDistance == math.MaxInt {
		gv.GraphDistance = 0
	} else {
		gv.GraphDistance = 1.0 / (1.0 + float64(bfsDistance))
	}

	// Signal 2 — Semantic similarity
	if c.Embeddings != nil && task != "" {
		gv.SemanticSimilarity = clamp01(c.Embeddings.Similarity(task, sym))
	}

	// Signal 3 — Recency (git mtime)
	// Signal 5 — Edit frequency
	stats := c.gitStats(sym.FilePath)
	gv.Recency = 1.0 / (1.0 + float64(stats.LastEditDays)/30.0)
	gv.EditFrequency = clamp01(float64(stats.CommitCount90d) / 20.0)

	// Signal 4 — Test relevance
	switch {
	case hasTestEdgeToSeed:
		gv.TestRelevance = 1.0
	case sameFileAsTest:
		gv.TestRelevance = 0.5
	default:
		gv.TestRelevance = 0.0
	}

	return gv
}

// gitStats returns recency + 90-day commit count for filePath relative to
// the workspace root. The 90-day window comes from one batched `git log`
// pass shared by every candidate; only files untouched in that window pay a
// cheap per-file recency lookup. Results are cached per path.
func (c *SignalComputer) gitStats(filePath string) gitFileStats {
	c.gitMu.Lock()
	defer c.gitMu.Unlock()
	if s, ok := c.gitCache[filePath]; ok {
		return s
	}
	c.loadRecentLocked()

	rel := filepath.ToSlash(filePath)
	if root := filepath.ToSlash(c.WorkspaceRoot); root != "" && strings.HasPrefix(rel, root+"/") {
		rel = strings.TrimPrefix(rel, root+"/")
	}
	if s, ok := c.recent[rel]; ok {
		c.gitCache[filePath] = s
		return s
	}

	// Not edited in the last 90 days: EditFrequency is 0 by definition; one
	// cheap per-file lookup (no --follow) resolves recency.
	s := gitFileStats{LastEditDays: 365, CommitCount90d: 0}
	if c.WorkspaceRoot != "" {
		out, err := runGit(c.WorkspaceRoot, "log", "-1", "--format=%ct", "--", rel)
		if err == nil {
			ts, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
			if perr == nil && ts > 0 {
				s.LastEditDays = int(time.Since(time.Unix(ts, 0)).Hours() / 24)
				if s.LastEditDays < 0 {
					s.LastEditDays = 0
				}
			}
		}
	}
	c.gitCache[filePath] = s
	return s
}

// loadRecentLocked runs the single batched history pass: every commit of the
// last 90 days with the files it touched. One subprocess for the whole
// ranking pass instead of two per candidate file.
func (c *SignalComputer) loadRecentLocked() {
	if c.gitLoaded {
		return
	}
	c.gitLoaded = true
	c.recent = map[string]gitFileStats{}
	if c.WorkspaceRoot == "" {
		return
	}
	out, err := runGit(c.WorkspaceRoot, "log", "--since=90.days", "--format=@%ct", "--name-only")
	if err != nil {
		return
	}
	now := time.Now()
	commitDays := 365
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "@"); ok {
			if ts, perr := strconv.ParseInt(rest, 10, 64); perr == nil && ts > 0 {
				commitDays = int(now.Sub(time.Unix(ts, 0)).Hours() / 24)
				if commitDays < 0 {
					commitDays = 0
				}
				continue
			}
		}
		s, seen := c.recent[line]
		s.CommitCount90d++
		// Output is newest-first, so the first sighting carries the most
		// recent edit; keep the minimum to be safe against ordering changes.
		if !seen || commitDays < s.LastEditDays {
			s.LastEditDays = commitDays
		}
		c.recent[line] = s
	}
}

// gitCommandTimeout bounds every git invocation. prism_query runs git per
// candidate symbol; without a hard cap a single slow or stuck git process (a
// pathological --follow history, a stalled filesystem, an unexpected prompt)
// would hang the whole request past its deadline. On timeout we return an
// error and the caller falls back to the neutral default signal values.
const gitCommandTimeout = 10 * time.Second

func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
