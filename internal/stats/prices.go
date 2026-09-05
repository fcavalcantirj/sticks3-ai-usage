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
// Cache write is billed at 1.25x the input rate (1x for the tokens written plus
// a 25% cache-creation surcharge), per the Anthropic pricing page.
//
//	Claude Fable 5       $10 / $50      cache_write $12.50  cache_read $1
//	Claude Fable 5.1     $10 / $50      cache_write $12.50  cache_read $0.25
//	Claude Opus 5        $5 / $25       cache_write $6.25  cache_read $0.50
//	Claude Opus 4.8      $5 / $25       cache_write $6.25  cache_read $0.50
//	Claude Sonnet 5      $2 / $10       cache_write $2.50  cache_read $0.20
//	Claude Sonnet 4.6    $3 / $15       cache_write $3.75  cache_read $0.30
//	Claude Sonnet 4.1    $15 / $75      cache_write $18.75  cache_read $1.50  (retired)
//	Claude Haiku 4.5     $1 / $5        cache_write $1.25  cache_read $0.10
//	Claude Haiku 3.5     $0.80 / $4     cache_write $1.00  cache_read $0.08  (retired)
//
// OpenRouter public pricing (https://openrouter.ai/models, 2026-09-04):
var prices = map[string]Price{
	// Claude Code — current models (from https://docs.anthropic.com/en/docs/about-claude/pricing, 2026-09-04)
	"claude-fable-5":             {10, 50, 1, 12.5},
	"claude-fable-5-1":           {10, 50, 0.25, 12.50},
	"claude-opus-5":              {5, 25, 0.50, 6.25},
	"claude-opus-4-8":            {5, 25, 0.50, 6.25},
	"claude-sonnet-5":            {2, 10, 0.20, 2.5},
	"claude-sonnet-4-20250514":   {3, 15, 0.30, 3.75},   // Sonnet 4.6
	"claude-sonnet-4-6":          {3, 15, 0.30, 3.75},   // Sonnet 4.6 (alt ID in transcripts)
	"claude-opus-4-20250514":     {15, 75, 1.50, 18.75}, // Opus 4.1 (retired)
	"claude-3-7-sonnet-20250219": {3, 15, 0.30, 3.75},   // Sonnet 4.6
	"claude-3-5-sonnet-20241022": {3, 15, 0.30, 3.75},   // Sonnet 4 (retired)
	"claude-3-5-haiku-20241022":  {0.80, 4, 0.08, 1.0},  // Haiku 3.5 (retired)
	"claude-haiku-4-5-20251001":  {1, 5, 0.10, 1.25},    // Haiku 4.5

	// OpenAI model pricing from models.dev/api.json (https://models.dev/api.json,
	// fetched 2026-09-04). Rates are USD per 1M tokens. cache_write is 0 because
	// Codex/OpenAI rollouts do not use prompt caching.
	"gpt-4.1":      {2, 8, 0.5, 0},
	"gpt-4.1-mini": {0.4, 1.6, 0.1, 0},
	"gpt-4o":       {2.5, 10, 1.25, 0},
	"gpt-4o-mini":  {0.15, 0.6, 0.075, 0},
	"o1":           {15, 60, 7.5, 0},
	"o1-preview":   {15, 60, 7.5, 0},
	"o1-mini":      {3, 12, 1.5, 0},
	"o3":           {6, 24, 1.5, 0},
	"o3-mini":      {1.5, 6, 0.75, 0},

	// openai/gpt-5.x — Codex Plus-subscription models. Per ORDER #40/#41, use the
	// CANONICAL openai/* entries from models.dev (not cloud-reseller mirrors).
	// gpt-5.6-sol publishes cache_write=5 (real value — NOT "free"/"unpriced").
	// gpt-5.5 has no cache_write published (Codex does not bill cache creation).
	// Both gpt-5.5 and gpt-5.6-sol also have a context_over_200k tier at ~2x; ignored.
	"openai/gpt-5.5":     {5, 30, 0.5, 0},
	"openai/gpt-5.6-sol": {4, 20, 0.4, 5},
	"openai/fugu-ultra":  {5, 30, 0.5, 0},
	// NOTE: bare "fugu" has no entry on models.dev — left unpriced (no alias).

	// Qwen / Llama identifiers that appear in Codex rollouts, priced from
	// OpenRouter's public table (https://openrouter.ai/models, 2026-09-04).
	"qwen/qwen-3-32b":          {0.2, 0.6, 0, 0},
	"meta-llama/llama-4-scout": {0.18, 0.55, 0, 0},
}

// aliasModels maps bare/old model ID aliases to their current family default.
// These are common in transcript data where the model string is abbreviated.
var aliasModels = map[string]string{
	"opus":        "claude-opus-5",
	"sonnet":      "claude-sonnet-5",
	"gpt-5.5":     "openai/gpt-5.5",
	"gpt-5.6-sol": "openai/gpt-5.6-sol",
	"fugu-ultra":  "openai/fugu-ultra",
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
