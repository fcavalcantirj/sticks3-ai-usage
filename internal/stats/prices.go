package stats

// Price is the per-1M-token cost in USD for a model.
type Price struct {
	Input      float64 `json:"input"`       // USD per 1M input tokens
	Output     float64 `json:"output"`      // USD per 1M output tokens
	CacheRead  float64 `json:"cache_read"`  // USD per 1M cache-read tokens (0 if not discounted)
	CacheWrite float64 `json:"cache_write"` // USD per 1M cache-write tokens (cache_creation_input_tokens)
}

// Cost multiplies a token count by the per-1M rate.
func (p Price) Cost(t Tokens) float64 {
	return (float64(t.Input)*p.Input +
		float64(t.Output)*p.Output +
		float64(t.CacheRead)*p.CacheRead +
		float64(t.CacheWrite)*p.CacheWrite) / 1e6
}

// prices maps model identifiers to their per-1M-token USD rates.
// Sources and dates are in the comments below.
//
// Claude (as of 2026-09-04, https://www.anthropic.com/pricing):
//   - claude-sonnet-4-20250514: 3.00 / 15.00
//   - claude-opus-4-20250514:   15.00 / 75.00
//   - claude-3-7-sonnet-20250219: 3.00 / 15.00
//   - claude-3-5-sonnet-20241022: 3.00 / 15.00
//   - claude-3-5-haiku-20241022: 0.80 / 4.00
//
// Open-source models priced from OpenRouter's public table
// (https://openrouter.ai/models, 2026-09-04).
var prices = map[string]Price{
	// Claude Code
	"claude-sonnet-4-20250514":   {0.003, 0.015, 0.00075, 0},
	"claude-opus-4-20250514":     {0.015, 0.075, 0.00375, 0},
	"claude-3-7-sonnet-20250219": {0.003, 0.015, 0.00075, 0},
	"claude-3-5-sonnet-20241022": {0.003, 0.015, 0.00075, 0},
	"claude-3-5-haiku-20241022":  {0.0008, 0.004, 0.0002, 0},

	// Open-source models that appear in Codex rollouts, priced from
	// OpenRouter's public table (https://openrouter.ai/models, 2026-09-04).
	"gpt-4.1":      {0.002, 0.008, 0, 0},
	"gpt-4.1-mini": {0.0004, 0.0016, 0, 0},
	"gpt-4o":       {0.005, 0.015, 0, 0},
	"gpt-4o-mini":  {0.00015, 0.0006, 0, 0},
	"o1":           {0.015, 0.06, 0, 0},
	"o1-preview":   {0.015, 0.06, 0, 0},
	"o1-mini":      {0.003, 0.012, 0, 0},
	"o3":           {0.006, 0.024, 0, 0},
	"o3-mini":      {0.0015, 0.006, 0, 0},

	// Codex may emit these identifiers; priced from OpenRouter.
	"qwen/qwen-3-32b":          {0.0002, 0.0006, 0, 0},
	"meta-llama/llama-4-scout": {0.00018, 0.00055, 0, 0},
}

// LookupPrice returns the price for a model ID and whether it was found.
// Unknown models return Price{}, false.
func LookupPrice(model string) (Price, bool) {
	p, ok := prices[model]
	return p, ok
}
