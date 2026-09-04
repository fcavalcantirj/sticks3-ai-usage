package stats

import "testing"

// TestAnthropicCacheWriteTripWire pins the invariant that every Anthropic
// cache_write price is 1.2–1.3× its input rate (the real 1.25× multiplier from
// the Anthropic pricing page). This fails if someone reverts to a 2× heuristic
// or forgets the cache-creation surcharge — see BUG 44b / ORDER #40.
func TestAnthropicCacheWriteTripWire(t *testing.T) {
	anthropicPrefixes := []string{"claude-fable", "claude-opus", "claude-sonnet", "claude-haiku", "claude-3"}
	for model, p := range prices {
		isAnthropic := false
		for _, prefix := range anthropicPrefixes {
			if model == prefix || startsWith(model, prefix+"-") || startsWith(model, prefix+"_") {
				isAnthropic = true
				break
			}
		}
		if !isAnthropic {
			continue
		}
		if p.Input <= 0 {
			t.Errorf("cache_write trip-wire: %s has Input=%v (should be >0)", model, p.Input)
			continue
		}
		ratio := p.CacheWrite / p.Input
		if ratio < 1.2 || ratio > 1.3 {
			t.Errorf("cache_write trip-wire: %s cache_write=%v is %.2fx input (expected ~1.25x, range 1.2-1.3)",
				model, p.CacheWrite, ratio)
		}
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestOpenAICacheWriteNeverCharged pins the invariant that OpenAI/Codex models
// have zero cache_write cost, because Codex rollouts do not bill cache creation.
// Exception: openai/gpt-5.6-sol publishes cache_write=5 on models.dev (per
// ORDER #40/CORRECTION seq 208) — that is a real rate, not "free" or "unpriced".
func TestOpenAICacheWriteNeverCharged(t *testing.T) {
	openPrefixes := []string{"gpt-", "o1", "o3", "qwen/", "meta-llama/", "openai/"}
	// Models whose cache_write is non-zero per models.dev (not "free").
	nonZero := map[string]float64{
		"openai/gpt-5.6-sol": 5,
	}
	for model, p := range prices {
		isOpenAI := false
		for _, prefix := range openPrefixes {
			if model == prefix || startsWith(model, prefix) {
				isOpenAI = true
				break
			}
		}
		if !isOpenAI {
			continue
		}
		if expected, ok := nonZero[model]; ok {
			if p.CacheWrite != expected {
				t.Errorf("OpenAI cache_write: %s cache_write=%v (expected %.1f per models.dev, NOT 0)",
					model, p.CacheWrite, expected)
			}
			continue
		}
		if p.CacheWrite != 0 {
			t.Errorf("OpenAI cache_write trip-wire: %s cache_write=%v (expected 0, no prompt caching in Codex)",
				model, p.CacheWrite)
		}
	}
}
