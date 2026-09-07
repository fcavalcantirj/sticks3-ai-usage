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
// The two subscription plans default to the vendors' published BRL list prices
// (see PlanPresets for the sources), each carrying the USD equivalent the
// vendor itself prints so the API-equiv ratio needs no invented exchange rate.
func DefaultProviders() []DefaultProvider {
	return []DefaultProvider{
		{
			ID:      ProviderClaude,
			Enabled: true,
			Label:   "Claude",
			Plan:    &PlanConfig{Cost: 1100, Currency: "BRL", CostUSD: 200, HasCostUSD: true, Label: "Max 20x"},
		},
		{
			ID:      ProviderCodex,
			Enabled: true,
			Label:   "ChatGPT",
			Plan:    &PlanConfig{Cost: 110, Currency: "BRL", CostUSD: 20, HasCostUSD: true, Label: "Plus"},
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
			CanProbe: d.ID == ProviderGroq, // ORDER #54 task 59: Groq is the only provider with a probe
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
// subscription plan. CanProbe is true when the provider supports a probe
// toggle (ORDER #54 task 59: Groq is the only one).
type EffectiveProvider struct {
	ID       string
	Enabled  bool
	Label    string
	KeyEnv   string // env var name that supplies the key, "" when none
	Probe    bool
	HasProbe bool
	CanProbe bool // true when the provider supports a probe toggle (Groq)
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

// --- Published plan prices ---------------------------------------------------

// PlanPresets returns the subscription tiers each provider publishes, keyed by
// provider id, in the order a picker should show them.
//
// WHY THESE ARE BUILT IN RATHER THAN CONFIGURED. A plan's price is a published
// list price: it is the same for every user of usaged, nobody can look it up
// faster than we can ship it, and asking each person to hand-write cost,
// currency and label into a YAML file is three chances to get their own bill
// wrong. Settings offers the list; typing stays possible for anyone on a plan
// that is not here (legacy pricing, an enterprise agreement, a currency we do
// not list).
//
// PRICES ARE IN BRL, WITH THE USD EQUIVALENT THE VENDOR ITSELF PRINTS. Both
// pages quote a local price and a USD one; carrying both means the API-equiv
// ratio can be computed without inventing an exchange rate — the rule
// config.example.yaml already states ("when absent for non-USD plans, no ratio
// is shown — no invented FX").
//
// SOURCES, read 2026-09-07:
//   - anthropic.com pricing: Pro "R$110 if billed monthly"; Max "From R$550 per
//     month", "Choose 5x or 20x more usage than Pro" — the 20x tier is double
//     the 5x one.
//   - openai.com/business/pricing (ChatGPT tab): Standard seat R$100/month
//     ("$25/month if billed monthly"); Premium seat R$500/month ("$125/month if
//     billed monthly", "5x more usage than standard, with no 5-hour limit").
//
// THESE GO STALE. They are list prices on someone else's page, so treat a
// mismatch with a real invoice as this table being out of date, not the bill.
func PlanPresets() map[string][]PlanConfig {
	return map[string][]PlanConfig{
		ProviderClaude: {
			{Cost: 110, Currency: "BRL", CostUSD: 20, HasCostUSD: true, Label: "Pro"},
			{Cost: 550, Currency: "BRL", CostUSD: 100, HasCostUSD: true, Label: "Max 5x"},
			{Cost: 1100, Currency: "BRL", CostUSD: 200, HasCostUSD: true, Label: "Max 20x"},
		},
		ProviderCodex: {
			{Cost: 110, Currency: "BRL", CostUSD: 20, HasCostUSD: true, Label: "Plus"},
			{Cost: 100, Currency: "BRL", CostUSD: 25, HasCostUSD: true, Label: "Business Standard"},
			{Cost: 500, Currency: "BRL", CostUSD: 125, HasCostUSD: true, Label: "Business Premium"},
		},
	}
}
