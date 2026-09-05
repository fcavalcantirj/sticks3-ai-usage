package config

// This file defines the five built-in providers every usaged knows about.
// They are the fallback when config.yaml lists none (ORDER #52a): GET /v1/config
// must always return all five so the settings page never renders an empty
// Providers section. File entries override these defaults by id; a provider
// absent from the file falls back to its built-in default.

// Provider id constants match the snapshot v1 contract
// (internal/snapshot/types.go canonicalProviderOrder).
const (
	ProviderClaude         = "claude"
	ProviderCodex          = "codex"
	ProviderOpenRouterMain = "openrouter:main"
	ProviderOpenRouterFbk  = "openrouter:fallback"
	ProviderGroq           = "groq"
)

// DefaultProviderOrder is the fixed display order used by the settings page
// and the GET /v1/config response.
var DefaultProviderOrder = []string{
	ProviderClaude,
	ProviderCodex,
	ProviderOpenRouterMain,
	ProviderOpenRouterFbk,
	ProviderGroq,
}

// DefaultProvider describes one built-in provider default. It is the same
// shape the YAML parser produces (YamlProvider), so a file override can be
// merged field-by-field.
type DefaultProvider = YamlProvider

// DefaultProviders returns the five built-in provider defaults in display order.
// These match config.example.yaml so the plan-value ratio works out of the
// box: Claude Max 20x ($200 USD) and ChatGPT Plus (R$110 BRL).
func DefaultProviders() []DefaultProvider {
	return []DefaultProvider{
		{
			ID:      ProviderClaude,
			Enabled: true,
			Label:   "Claude",
			Plan:    &PlanConfig{Cost: 200, Currency: "USD", Label: "Max 20x"},
		},
		{
			ID:      ProviderCodex,
			Enabled: true,
			Label:   "ChatGPT",
			Plan:    &PlanConfig{Cost: 110, Currency: "BRL", Label: "Plus"},
		},
		{
			ID:      ProviderOpenRouterMain,
			Enabled: true,
			Label:   "OpenRouter main",
			KeyEnv:  "OPENROUTER_API_KEY",
		},
		{
			ID:      ProviderOpenRouterFbk,
			Enabled: true,
			Label:   "OpenRouter fallback",
			KeyEnv:  "OPENROUTER_API_KEY_FALLBACK",
		},
		{
			ID:       ProviderGroq,
			Enabled:  true,
			Label:    "Groq",
			KeyEnv:   "GROQ_API_KEY",
			Probe:    false,
			HasProbe: false,
		},
	}
}

// defaultProviderByID returns the built-in default for id, or nil if id is not
// a known provider. The returned provider is a copy.
func defaultProviderByID(id string) *DefaultProvider {
	for i := range defaultProvidersCache {
		if defaultProvidersCache[i].ID == id {
			p := defaultProvidersCache[i]
			return &p
		}
	}
	return nil
}

var defaultProvidersCache = DefaultProviders()

// DefaultProviderByID is the exported lookup used by the PUT handler and tests.
func DefaultProviderByID(id string) *DefaultProvider {
	return defaultProviderByID(id)
}

// providerEqual compares the semantic fields of two YamlProviders for the
// purpose of deciding whether a file entry differs from the built-in default.
// HasProbe/HasCostUSD are parse-time disambiguators and are ignored — only the
// effective values (Enabled, Label, KeyEnv, Probe, Plan) matter.
func providerEqual(a, b YamlProvider) bool {
	return a.ID == b.ID &&
		a.Enabled == b.Enabled &&
		a.Label == b.Label &&
		a.KeyEnv == b.KeyEnv &&
		a.Probe == b.Probe &&
		planEqual(a.Plan, b.Plan)
}

func planEqual(a, b *PlanConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Cost == b.Cost && a.Currency == b.Currency &&
		a.CostUSD == b.CostUSD && a.Label == b.Label
}

// OverridesOnly returns the slice of payload providers that differ from their
// built-in defaults. Each returned provider is the fully-merged value (defaults
// + overrides) so the serialized YAML round-trips: loading it back and merging
// with defaults reproduces the same effective provider set. Providers matching
// their default exactly are dropped so the config file stays minimal.
func OverridesOnly(provs []YamlProvider) []YamlProvider {
	var out []YamlProvider
	for _, p := range provs {
		d := DefaultProviderByID(p.ID)
		if d == nil {
			// Unknown provider (not one of the five built-ins): keep as-is.
			out = append(out, p)
			continue
		}
		eff := mergeWithDefault(p, *d)
		if !providerEqual(eff, *d) {
			out = append(out, eff)
		}
	}
	return out
}

// mergeWithDefault fills unset/zero fields of p from the default d so the
// returned provider is a complete effective record.
func mergeWithDefault(p, d YamlProvider) YamlProvider {
	merged := d
	merged.Enabled = p.Enabled
	if p.Label != "" {
		merged.Label = p.Label
	}
	if p.KeyEnv != "" {
		merged.KeyEnv = p.KeyEnv
	}
	if p.HasProbe {
		merged.Probe = p.Probe
		merged.HasProbe = true
	}
	if p.Plan != nil {
		merged.Plan = p.Plan
	}
	// If the payload had a Plan but with zero values, the parser already set it
	// non-nil; keep it. If the default had a plan and the payload didn't, keep
	// the default's plan (already set above).
	return merged
}

// EffectiveProviders returns the five built-in providers in display order,
// with any file-configured overrides layered on top. A file entry overrides
// only the fields it names; fields left blank fall back to the default.
func (c Config) EffectiveProviders() []EffectiveProvider {
	defs := DefaultProviders()
	out := make([]EffectiveProvider, 0, len(defs))
	for _, d := range defs {
		merged := EffectiveProvider{
			ID:       d.ID,
			Enabled:  d.Enabled,
			Label:    d.Label,
			KeyEnv:   d.KeyEnv,
			Probe:    d.Probe,
			HasProbe: d.HasProbe,
			Plan:     d.Plan,
		}
		if file, ok := c.ProviderConfigs[d.ID]; ok {
			// File entry overrides: Enabled is always set by the parser (defaults
			// true), so use it directly. For other fields, only override when the
			// file names a non-empty / explicitly-set value.
			merged.Enabled = file.Enabled
			if file.Label != "" {
				merged.Label = file.Label
			}
			if file.KeyEnv != "" {
				merged.KeyEnv = file.KeyEnv
			}
			if file.HasProbe {
				merged.HasProbe = true
				merged.Probe = file.Probe
			}
			if file.Plan != nil {
				merged.Plan = file.Plan
			}
		}
		out = append(out, merged)
	}
	return out
}

// EffectiveProvider is a single provider's resolved settings, shown on the
// settings page and returned by GET /v1/config. It combines built-in defaults
// with any file overrides. The Plan pointer is nil when the provider has no
// subscription plan.
type EffectiveProvider struct {
	ID       string
	Enabled  bool
	Label    string
	KeyEnv   string // env var name that supplies the key, "" when none
	Probe    bool
	HasProbe bool
	Plan     *PlanConfig
}

// GroqProbeEnabled reports whether the Groq probe toggle is on. It prefers the
// per-provider YAML setting (ProviderConfigs["groq"].Probe) over the legacy
// top-level GroqProbe env var, so the settings page probe toggle actually
// drives polling.
func (c Config) GroqProbeEnabled() bool {
	if p, ok := c.ProviderConfigs[ProviderGroq]; ok && p.HasProbe {
		return p.Probe
	}
	return c.GroqProbe
}
