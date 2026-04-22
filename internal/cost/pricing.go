package cost

import (
	"fmt"
	"strings"
)

// Rates holds per-model pricing in USD per million tokens.
type Rates struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// Usage holds aggregated token counts.
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	Model               string
	AssistantTurns      int
}

var ErrUnknownModel = fmt.Errorf("unknown model")

// pricing is the static pricing table for Claude models.
// Prices in USD per million tokens (as of 2025).
var pricing = map[string]Rates{
	"claude-opus-4-6": {
		InputPerMTok:      15.0,
		OutputPerMTok:     75.0,
		CacheWritePerMTok: 18.75,
		CacheReadPerMTok:  1.50,
	},
	"claude-sonnet-4-6": {
		InputPerMTok:      3.0,
		OutputPerMTok:     15.0,
		CacheWritePerMTok: 3.75,
		CacheReadPerMTok:  0.30,
	},
	"claude-haiku-4-5": {
		InputPerMTok:      0.80,
		OutputPerMTok:     4.0,
		CacheWritePerMTok: 1.0,
		CacheReadPerMTok:  0.08,
	},
	// Older model IDs that may appear in transcripts
	"claude-sonnet-4-5-20250514": {
		InputPerMTok:      3.0,
		OutputPerMTok:     15.0,
		CacheWritePerMTok: 3.75,
		CacheReadPerMTok:  0.30,
	},
}

// Cost calculates the USD cost for a given model and usage.
func Cost(model string, u Usage) (float64, error) {
	rates, ok := lookupRates(model)
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownModel, model)
	}

	cost := float64(u.InputTokens)/1_000_000*rates.InputPerMTok +
		float64(u.OutputTokens)/1_000_000*rates.OutputPerMTok +
		float64(u.CacheCreationTokens)/1_000_000*rates.CacheWritePerMTok +
		float64(u.CacheReadTokens)/1_000_000*rates.CacheReadPerMTok

	return cost, nil
}

// AllModels returns all known model identifiers.
func AllModels() []string {
	models := make([]string, 0, len(pricing))
	for m := range pricing {
		models = append(models, m)
	}
	return models
}

func lookupRates(model string) (Rates, bool) {
	// Exact match
	if r, ok := pricing[model]; ok {
		return r, true
	}
	// Prefix match (e.g., "claude-opus-4-6[1m]" -> "claude-opus-4-6")
	for key, r := range pricing {
		if strings.HasPrefix(model, key) {
			return r, true
		}
	}
	return Rates{}, false
}
