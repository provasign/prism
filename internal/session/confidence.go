package session

// Confidence is the qualitative judgement of whether previously-delivered
// content is still likely visible in the model's context window.
type Confidence string

const (
	High   Confidence = "high"
	Medium Confidence = "medium"
	Low    Confidence = "low"
)

// EstimateConfidence returns the confidence that a previously-sent item is
// still in the model's window, given how many tokens were delivered since
// it was sent and the total context window size.
func EstimateConfidence(tokensSince int64, contextWindow int) Confidence {
	if contextWindow <= 0 {
		return Low
	}
	ratio := float64(tokensSince) / float64(contextWindow)
	switch {
	case ratio < 0.3:
		return High
	case ratio < 0.7:
		return Medium
	default:
		return Low
	}
}
