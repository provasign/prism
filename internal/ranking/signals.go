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

	"github.com/tabladrum/grove-suite/prism/internal/grove"
)

// SignalComputer computes individual signal values for a symbol relative to
// a task and a workspace root.
type SignalComputer struct {
	WorkspaceRoot string
	Embeddings    SemanticBackend // optional; if nil, similarity is 0

	gitMu      sync.Mutex
	gitCache   map[string]gitFileStats
	maxSeenCnt int
}

// SemanticBackend computes cosine similarity between a task and a symbol's
// descriptive text. TF-IDF and ONNX backends both satisfy this.
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
// the workspace root. Caches per-path within the computer.
func (c *SignalComputer) gitStats(filePath string) gitFileStats {
	c.gitMu.Lock()
	defer c.gitMu.Unlock()
	if s, ok := c.gitCache[filePath]; ok {
		return s
	}
	s := gitFileStats{LastEditDays: 365, CommitCount90d: 0}
	if c.WorkspaceRoot == "" {
		c.gitCache[filePath] = s
		return s
	}
	abs := filePath
	if !filepath.IsAbs(filePath) {
		abs = filepath.Join(c.WorkspaceRoot, filePath)
	}
	// Last commit unix time on this file.
	out, err := runGit(c.WorkspaceRoot, "log", "-1", "--format=%ct", "--", abs)
	if err == nil {
		ts, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
		if perr == nil && ts > 0 {
			s.LastEditDays = int(time.Since(time.Unix(ts, 0)).Hours() / 24)
			if s.LastEditDays < 0 {
				s.LastEditDays = 0
			}
		}
	}
	// 90-day commit count.
	out, err = runGit(c.WorkspaceRoot, "log", "--since=90.days", "--oneline", "--follow", "--", abs)
	if err == nil {
		// TrimSpace before counting so a missing trailing newline (possible in
		// piped environments) does not undercount a single-commit file as zero.
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			s.CommitCount90d = strings.Count(trimmed, "\n") + 1
		}
	}
	c.gitCache[filePath] = s
	return s
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
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
