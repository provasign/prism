package session

import "testing"

func TestEstimateConfidence_ZeroWindow(t *testing.T) {
	if got := EstimateConfidence(0, 0); got != Low {
		t.Errorf("contextWindow=0: want Low, got %s", got)
	}
}

func TestEstimateConfidence_NegativeWindow(t *testing.T) {
	if got := EstimateConfidence(100, -1); got != Low {
		t.Errorf("contextWindow=-1: want Low, got %s", got)
	}
}

func TestEstimateConfidence_High(t *testing.T) {
	// ratio = 200/1000 = 0.2 < 0.3 → High
	if got := EstimateConfidence(200, 1000); got != High {
		t.Errorf("ratio=0.2: want High, got %s", got)
	}
}

func TestEstimateConfidence_Medium(t *testing.T) {
	// ratio = 500/1000 = 0.5, in [0.3, 0.7) → Medium
	if got := EstimateConfidence(500, 1000); got != Medium {
		t.Errorf("ratio=0.5: want Medium, got %s", got)
	}
}

func TestEstimateConfidence_Low(t *testing.T) {
	// ratio = 800/1000 = 0.8 >= 0.7 → Low
	if got := EstimateConfidence(800, 1000); got != Low {
		t.Errorf("ratio=0.8: want Low, got %s", got)
	}
}

func TestEstimateConfidence_BoundaryHigh(t *testing.T) {
	// ratio = 299/1000 = 0.299 → just below 0.3 → High
	if got := EstimateConfidence(299, 1000); got != High {
		t.Errorf("ratio=0.299: want High, got %s", got)
	}
}

func TestEstimateConfidence_BoundaryMedium(t *testing.T) {
	// ratio = 300/1000 = 0.3 → exactly at boundary → Medium
	if got := EstimateConfidence(300, 1000); got != Medium {
		t.Errorf("ratio=0.3: want Medium, got %s", got)
	}
}

func TestEstimateConfidence_BoundaryLow(t *testing.T) {
	// ratio = 700/1000 = 0.7 → exactly at boundary → Low
	if got := EstimateConfidence(700, 1000); got != Low {
		t.Errorf("ratio=0.7: want Low, got %s", got)
	}
}
