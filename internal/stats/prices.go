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
// Sources and dates are in the comments below. Rates are USD per 1M tokens.
//
// Claude (as of 2026-09-04, https://docs.anthropic.com/en/docs/about-claude/pricing):
//
//	Claude Fable 5       $10 / $50      cache_write $20  cache_read $1
//	Claude Fable 5.1     $10 / $50      cache_write $12.50  cache_read $0.25
//	Claude Opus 5        $5 / $25       cache_write $10  cache_read $0.50
//	Claude Opus 4.8      $5 / $25       cache_write $10  cache_read $0.50
//	Claude Sonnet 5      $2 / $10       cache_write $4  cache_read $0.20
//	Claude Sonnet 4.6    $3 / $15       cache_write $6  cache_read $0.30
//	Claude Sonnet 4.1    $15 / $75      cache_write $30  cache_read $1.50  (retired)
//	Claude Haiku 4.5     $1 / $5        cache_write $2  cache_read $0.10
//	Claude Haiku 3.5     $0.80 / $4     cache_write $1.60  cache_read $0.08  (retired)
//
// OpenRouter public pricing (https://openrouter.ai/models, 2026-09-04):
var prices = map[string]Price{
	// Claude Code — current models (from https://docs.anthropic.com/en/docs/about-claude/pricing, 2026-09-04)
	"claude-fable-5":             {10, 50, 1, 20},
	"claude-fable-5-1":           {10, 50, 0.25, 12.50},
	"claude-opus-5":              {5, 25, 0.50, 10},
	"claude-opus-4-8":            {5, 25, 0.50, 10},
	"claude-sonnet-5":            {2, 10, 0.20, 4},
	"claude-sonnet-4-20250514":   {3, 15, 0.30, 6},      // Sonnet 4.6
	"claude-opus-4-20250514":     {15, 75, 1.50, 30},    // Opus 4.1 (retired)
	"claude-3-7-sonnet-20250219": {3, 15, 0.30, 6},      // Sonnet 4.6
	"claude-3-5-sonnet-20241022": {3, 15, 0.30, 6},      // Sonnet 4 (retired)
	"claude-3-5-haiku-20241022":  {0.80, 4, 0.08, 1.60}, // Haiku 3.5 (retired)
	"claude-haiku-4-5-20251001":  {1, 5, 0.10, 2},       // Haiku 4.5

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

// aliasModels maps bare/old model ID aliases to their current family default.
// These are common in transcript data where the model string is abbreviated.
var aliasModels = map[string]string{
	"opus":   "claude-opus-5",
	"sonnet": "claude-sonnet-5",
}

// LookupPrice resolves a model ID to its Price. It checks the prices table
// directly, then falls back to alias mapping for bare/opaque model names.
// Unknown models return Price{}, false.
func LookupPrice(model string) (Price, bool) {
	if p, ok := prices[model]; ok {
		return p, true
	}
	if alias, ok := aliasModels[model]; ok {
		p, ok2 := prices[alias]
		return p, ok2
	}
	return Price{}, false
}

// IsPricedModel returns whether a model has a known price.
// "<synthetic>" is always unpriced (excluded from cost and model table).
func IsPricedModel(model string) bool {
	if model == "<synthetic>" {
		return false
	}
	_, ok := LookupPrice(model)
	return ok
}
