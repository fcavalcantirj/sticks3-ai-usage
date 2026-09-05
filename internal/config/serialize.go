package config

import (
	"fmt"
	"strconv"
)

// ValidAlertKey reports whether k is a recognised alert threshold key.
func ValidAlertKey(k string) bool {
	return validAlertKeys[k]
}

// SerializeYAML renders a FileConfig back into the YAML subset that
// ParseYAML accepts. The output is deterministic: providers in the order
// given, plan block indented under each provider.
func SerializeYAML(fc FileConfig) (string, error) {
	var out string

	if fc.IntervalSec > 0 {
		out += fmt.Sprintf("interval_sec: %d\n", fc.IntervalSec)
	}
	if fc.Listen != "" {
		out += fmt.Sprintf("listen: %s\n", yamlStr(fc.Listen))
	}
	if fc.TZ != "" {
		out += fmt.Sprintf("tz: %s\n", yamlStr(fc.TZ))
	}

	if len(fc.Alerts) > 0 {
		out += "alerts:\n"
		for _, key := range []string{"openrouter_low_usd", "quota_warn_pct"} {
			if v, ok := fc.Alerts[key]; ok {
				out += fmt.Sprintf("  %s: %s\n", key, strconv.FormatFloat(v, 'f', -1, 64))
			}
		}
	}

	if len(fc.Providers) > 0 {
		out += "providers:\n"
		for _, p := range fc.Providers {
			out += fmt.Sprintf("- id: %s\n", yamlStr(p.ID))
			// enabled: only emit if false (default is true).
			if !p.Enabled {
				out += "  enabled: false\n"
			}
			if p.Label != "" {
				out += fmt.Sprintf("  label: %s\n", yamlStr(p.Label))
			}
			if p.KeyEnv != "" {
				out += fmt.Sprintf("  key_env: %s\n", yamlStr(p.KeyEnv))
			}
			if p.HasProbe {
				out += fmt.Sprintf("  probe: %t\n", p.Probe)
			}
			if p.Plan != nil {
				out += "  plan:\n"
				if p.Plan.Cost != 0 {
					out += fmt.Sprintf("    cost: %s\n", strconv.FormatFloat(p.Plan.Cost, 'f', -1, 64))
				}
				if p.Plan.Currency != "" {
					out += fmt.Sprintf("    currency: %s\n", yamlStr(p.Plan.Currency))
				}
				if p.Plan.Label != "" {
					out += fmt.Sprintf("    label: %s\n", yamlStr(p.Plan.Label))
				}
				if p.Plan.HasCostUSD {
					out += fmt.Sprintf("    cost_usd: %s\n", strconv.FormatFloat(p.Plan.CostUSD, 'f', -1, 64))
				}
			}
		}
	}

	return out, nil
}

// yamlStr quotes a string value if it contains special YAML characters.
func yamlStr(s string) string {
	if s == "" {
		return `""`
	}
	for _, c := range s {
		if c == ' ' || c == ':' || c == '#' || c == '"' || c == '\'' || c == '{' || c == '}' || c == '[' || c == ']' || c == ',' || c == '&' || c == '*' || c == '?' || c == '|' || c == '-' || c == '<' || c == '>' {
			return strconv.Quote(s)
		}
	}
	return s
}
