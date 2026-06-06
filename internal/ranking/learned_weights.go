package ranking

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// LearnedWeights stores per-repo adjustments layered on top of base Profiles.
// Each weight is a nudge in [-1, +1] applied multiplicatively to the base
// profile weight. Positive = reinforce, negative = suppress.
//
// Keys are Profile.Name (e.g. "fix_bug"), values are per-signal adjustments.
type LearnedWeights struct {
	mu      sync.RWMutex
	path    string
	weights map[string]SignalValues // profileName → adjustment vector
}

// weightFile is the on-disk JSON format.
type weightFile struct {
	RepoKey string                  `json:"repoKey"`
	Weights map[string]SignalValues `json:"weights"`
}

// weightsDir returns the user-cache path for learned weights.
func weightsDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "prism", "weights"), nil
}

func repoKey(root string) string {
	h := sha1.New()
	h.Write([]byte(root))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LoadLearnedWeights loads the per-repo weight adjustments from disk.
// Returns an empty (non-nil) store on any error so callers never get nil.
func LoadLearnedWeights(repoRoot string) *LearnedWeights {
	lw := &LearnedWeights{
		weights: make(map[string]SignalValues),
	}
	dir, err := weightsDir()
	if err != nil {
		return lw
	}
	lw.path = filepath.Join(dir, repoKey(repoRoot)+".json")
	b, err := os.ReadFile(lw.path)
	if err != nil {
		return lw // cache miss — normal first run
	}
	var wf weightFile
	if err := json.Unmarshal(b, &wf); err != nil {
		return lw
	}
	lw.weights = wf.Weights
	return lw
}

// Apply blends learned adjustments into a base profile. Each signal weight is
// nudged by the stored delta, then clamped to [0.05, 1.0] so no signal is
// entirely silenced.
func (lw *LearnedWeights) Apply(base Profile) Profile {
	lw.mu.RLock()
	adj, ok := lw.weights[base.Name]
	lw.mu.RUnlock()
	if !ok {
		return base
	}
	p := base
	p.GraphDistance = clamp(base.GraphDistance+adj.GraphDistance, 0.05, 1.0)
	p.SemanticSimilarity = clamp(base.SemanticSimilarity+adj.SemanticSimilarity, 0.05, 1.0)
	p.Recency = clamp(base.Recency+adj.Recency, 0.05, 1.0)
	p.TestRelevance = clamp(base.TestRelevance+adj.TestRelevance, 0.05, 1.0)
	p.EditFrequency = clamp(base.EditFrequency+adj.EditFrequency, 0.05, 1.0)
	return p
}

// RecordOutcome updates stored weights from one completed task's evidence.
//
//   - citedPaths: file paths that appeared in the final diff/commit.
//   - deliveredPaths: all file paths Prism delivered for this query.
//   - missingTestSignal: true when a test gate fired (tests were needed but absent).
//   - profileName: the Profile used for the query (e.g. "fix_bug").
//
// Uses a small learning rate (0.02) so weights converge over ~50 tasks.
func (lw *LearnedWeights) RecordOutcome(profileName string, citedPaths, deliveredPaths []string, missingTestSignal bool) {
	const lr = 0.02
	const maxAdj = 0.40 // cap total adjustment per signal

	cited := make(map[string]bool, len(citedPaths))
	for _, p := range citedPaths {
		cited[p] = true
	}

	var adj SignalValues
	for _, p := range deliveredPaths {
		if cited[p] {
			// Delivered AND used → reinforce SemanticSimilarity (most direct proxy
			// for "was this the right content") and GraphDistance.
			adj.SemanticSimilarity += lr
			adj.GraphDistance += lr * 0.5
		} else {
			// Delivered but NOT used → suppress.
			adj.SemanticSimilarity -= lr * 0.5
		}
	}
	if missingTestSignal {
		// Test gate fired: the agent needed tests but we didn't deliver enough.
		// Boost TestRelevance so the next similar query allocates more budget to tests.
		adj.TestRelevance += lr * 2
	}

	lw.mu.Lock()
	cur := lw.weights[profileName]
	cur.GraphDistance = clamp(cur.GraphDistance+adj.GraphDistance, -maxAdj, maxAdj)
	cur.SemanticSimilarity = clamp(cur.SemanticSimilarity+adj.SemanticSimilarity, -maxAdj, maxAdj)
	cur.Recency = clamp(cur.Recency+adj.Recency, -maxAdj, maxAdj)
	cur.TestRelevance = clamp(cur.TestRelevance+adj.TestRelevance, -maxAdj, maxAdj)
	cur.EditFrequency = clamp(cur.EditFrequency+adj.EditFrequency, -maxAdj, maxAdj)
	lw.weights[profileName] = cur
	lw.mu.Unlock()

	lw.save()
}

// DetectFeedbackSource returns the best available signal source for the repo.
// Priority: Provasign (.provasign/provasign.yaml or legacy .provasign.yaml
// present) > git (any .git dir) > none.
func DetectFeedbackSource(repoRoot string) string {
	for _, rel := range []string{
		filepath.Join(".provasign", "provasign.yaml"),
		".provasign.yaml",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err == nil {
			return "provasign"
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err == nil {
		return "git"
	}
	return "none"
}

func (lw *LearnedWeights) save() {
	if lw.path == "" {
		return
	}
	lw.mu.RLock()
	wf := weightFile{Weights: lw.weights}
	lw.mu.RUnlock()
	b, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(lw.path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(lw.path, b, 0o600)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
