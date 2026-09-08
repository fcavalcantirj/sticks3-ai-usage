package config

import (
	"fmt"
	"strconv"
	"strings"
)

// FileConfig is the parsed contents of config.yaml. It sits beneath
// environment variables and flags in the precedence chain:
// flags > env > file > defaults.
type FileConfig struct {
	IntervalSec   int
	Listen        string
	DeviceToken   string
	TZ            string
	Alerts        map[string]float64
	Providers     []YamlProvider
	ProviderOrder []string
}

// YamlProvider is a single provider entry from the YAML config.
type YamlProvider struct {
	ID       string
	Enabled  bool
	Label    string
	KeyEnv   string
	Probe    bool
	HasProbe bool // distinguishes "probe: false" from "probe not set"
	Plan     *PlanConfig
}

// PlanConfig is the subscription-plan block for a provider. It describes what
// the user actually pays (so API-equivalent usage can be compared against it).
type PlanConfig struct {
	Cost       float64
	Currency   string // e.g. "USD", "BRL"
	CostUSD    float64
	HasCostUSD bool // distinguishes "cost_usd: 0" from absent
	Label      string
}

// yamlLine is a single lexed line.
type yamlLine struct {
	indent  int    // number of leading spaces
	content string // text after indentation, comment stripped
	lineNo  int    // 1-based source line number
}

// validAlertKeys are the only keys allowed under the "alerts:" map.
// quota_warn_pct is kept for migration only (applyFileConfig maps it to both
// new keys); the serializer never writes it.
var validAlertKeys = map[string]bool{
	"openrouter_low_usd":    true,
	"quota_warn_pct":        true,
	"quota_warn_5h_pct":     true,
	"quota_warn_weekly_pct": true,
}

func validAlertKey(k string) bool {
	return validAlertKeys[k]
}

// ParseYAML parses the restricted YAML subset used by config.yaml.
// It accepts exactly the constructs the schema needs and rejects
// everything else with a line number.
func ParseYAML(text string) (FileConfig, error) {
	var fc FileConfig
	fc.Alerts = map[string]float64{}

	lines, err := lexYAML(text)
	if err != nil {
		return fc, err
	}

	i := 0
	for i < len(lines) {
		ln := lines[i]
		if ln.indent != 0 {
			return fc, fmt.Errorf("line %d: unexpected indentation (expected top-level key at column 0)", ln.lineNo)
		}

		key, value, ok := splitKV(ln.content)
		if !ok {
			return fc, fmt.Errorf("line %d: expected 'key: value', got %q", ln.lineNo, ln.content)
		}

		switch key {
		case "interval_sec":
			if value == "" {
				return fc, fmt.Errorf("line %d: interval_sec requires a value", ln.lineNo)
			}
			v, err := strconv.Atoi(value)
			if err != nil {
				return fc, fmt.Errorf("line %d: interval_sec must be an integer, got %q", ln.lineNo, value)
			}
			fc.IntervalSec = v

		case "listen":
			if value == "" {
				return fc, fmt.Errorf("line %d: listen requires a value", ln.lineNo)
			}
			fc.Listen = unquote(value)

		case "device_token":
			if value == "" {
				return fc, fmt.Errorf("line %d: device_token requires a value", ln.lineNo)
			}
			fc.DeviceToken = unquote(value)

		case "tz":
			if value == "" {
				return fc, fmt.Errorf("line %d: tz requires a value", ln.lineNo)
			}
			fc.TZ = unquote(value)

		case "alerts":
			if value != "" {
				return fc, fmt.Errorf("line %d: 'alerts:' must start a nested map, got value %q", ln.lineNo, value)
			}
			i++
			am, j, err := parseScalarMap(lines, i)
			if err != nil {
				return fc, err
			}
			i = j - 1 // -1 because outer loop does i++
			for k, v := range am {
				fc.Alerts[k] = v
			}

		case "providers":
			if value != "" {
				return fc, fmt.Errorf("line %d: 'providers:' must start a list, got value %q", ln.lineNo, value)
			}
			i++
			provs, j, err := parseProviderList(lines, i)
			if err != nil {
				return fc, err
			}
			i = j - 1
			fc.Providers = provs

		case "provider_order":
			if value != "" {
				return fc, fmt.Errorf("line %d: 'provider_order:' must start a list, got value %q", ln.lineNo, value)
			}
			i++
			list, j, err := parseStringList(lines, i)
			if err != nil {
				return fc, err
			}
			i = j - 1
			fc.ProviderOrder = list

		default:
			return fc, fmt.Errorf("line %d: unknown key %q (known keys: interval_sec, listen, device_token, tz, alerts, providers, provider_order)", ln.lineNo, key)
		}
		i++
	}

	if err := fc.validate(); err != nil {
		return fc, err
	}
	return fc, nil
}

// validate checks semantic constraints on the parsed FileConfig.
func (fc FileConfig) validate() error {
	seen := map[string]bool{}
	for i, p := range fc.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider at index %d: missing id", i)
		}
		if seen[p.ID] {
			return fmt.Errorf("provider %q: duplicate id", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// lexYAML splits text into non-blank, non-comment lines and records
// indentation. Tabs are rejected with an error.
func lexYAML(text string) ([]yamlLine, error) {
	raw := strings.Split(text, "\n")
	out := make([]yamlLine, 0, len(raw))
	for idx, raw := range raw {
		lineNo := idx + 1

		if strings.ContainsRune(raw, '\t') {
			return nil, fmt.Errorf("line %d: tabs are not allowed, use spaces", lineNo)
		}

		content := stripInlineComment(raw)
		content = strings.TrimRight(content, " ")
		if content == "" {
			continue
		}

		trimmed := strings.TrimLeft(content, " ")
		indent := len(content) - len(trimmed)
		out = append(out, yamlLine{indent: indent, content: trimmed, lineNo: lineNo})
	}
	return out, nil
}

// stripInlineComment removes a trailing " #..." comment but preserves
// "#" inside double-quoted strings.
func stripInlineComment(line string) string {
	inQuote := false
	for i, r := range line {
		if r == '"' {
			inQuote = !inQuote
		}
		if r == '#' && !inQuote {
			return line[:i]
		}
	}
	return line
}

// splitKV splits "key: value" into key and value (unquoted). If the
// line has no colon it returns ok=false.
func splitKV(s string) (key, value string, ok bool) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:idx])
	value = strings.TrimSpace(s[idx+1:])
	return key, value, true
}

// unquote strips surrounding double quotes from a YAML scalar.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// parseScalarMap reads a block of "key: value" lines at a uniform
// indentation level, where every value must be a number. Used for
// the "alerts:" section. Returns the map and the index of the first
// line past the block.
func parseScalarMap(lines []yamlLine, pos int) (map[string]float64, int, error) {
	m := map[string]float64{}
	if pos >= len(lines) {
		return m, pos, fmt.Errorf("expected map entries after parent key")
	}

	indent := lines[pos].indent
	// This YAML subset requires exactly 2-space indentation for nested
	// maps under alerts: / providers:. Reject anything else early so
	// a stray 3-space indent doesn't silently parse.
	if indent != 2 {
		return m, pos, fmt.Errorf("line %d: expected indentation of 2 spaces under 'alerts:', got %d", lines[pos].lineNo, indent)
	}

	for pos < len(lines) && lines[pos].indent == indent {
		ln := lines[pos]
		key, value, ok := splitKV(ln.content)
		if !ok {
			return m, pos, fmt.Errorf("line %d: expected 'key: value', got %q", ln.lineNo, ln.content)
		}
		if value == "" {
			return m, pos, fmt.Errorf("line %d: nested map values must be scalars (got key %q with no value)", ln.lineNo, key)
		}
		if !validAlertKey(key) {
			return m, pos, fmt.Errorf("line %d: unknown alert key %q (known: openrouter_low_usd, quota_warn_5h_pct, quota_warn_weekly_pct)", ln.lineNo, key)
		}

		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return m, pos, fmt.Errorf("line %d: value for %q must be a number, got %q", ln.lineNo, key, value)
		}
		m[key] = f
		pos++
	}

	return m, pos, nil
}

// parseStringList reads a YAML list of scalar strings (e.g. "provider_order:").
// Each item is a "- value" line at a uniform 2-space indentation. Returns the
// list and the index past the block.
func parseStringList(lines []yamlLine, pos int) ([]string, int, error) {
	var list []string

	if pos >= len(lines) {
		return list, pos, nil
	}

	listIndent := lines[pos].indent
	if listIndent != 2 {
		return list, pos, fmt.Errorf("line %d: expected indentation of 2 spaces under list key, got %d", lines[pos].lineNo, listIndent)
	}

	for pos < len(lines) && lines[pos].indent == listIndent && strings.HasPrefix(lines[pos].content, "- ") {
		val := strings.TrimPrefix(lines[pos].content, "- ")
		list = append(list, unquote(strings.TrimSpace(val)))
		pos++
	}

	return list, pos, nil
}

// parseProviderList reads a YAML list of maps. Each item begins with
// "- key: value" at listIndent and may have continuation keys at
// listIndent+2. Returns the providers and the index past the block.
func parseProviderList(lines []yamlLine, pos int) ([]YamlProvider, int, error) {
	var providers []YamlProvider

	if pos >= len(lines) {
		return providers, pos, nil
	}

	listIndent := lines[pos].indent
	itemIndent := listIndent + 2 // continuation keys align past "- "

	for pos < len(lines) && lines[pos].indent == listIndent && strings.HasPrefix(lines[pos].content, "- ") {
		ln := lines[pos]
		rest := strings.TrimPrefix(ln.content, "- ")

		prov := YamlProvider{Enabled: true} // enabled defaults to true

		// Parse the first key: value on the "- " line
		key, value, ok := splitKV(rest)
		if ok {
			if err := setProviderField(&prov, key, value, ln.lineNo); err != nil {
				return providers, pos, err
			}
		}

		pos++

		// Parse continuation keys at itemIndent
		for pos < len(lines) && lines[pos].indent == itemIndent {
			cl := lines[pos]
			k, v, ok := splitKV(cl.content)
			if !ok {
				return providers, pos, fmt.Errorf("line %d: expected 'key: value' in provider list, got %q", cl.lineNo, cl.content)
			}

			// A "plan:" line with no value starts a nested block.
			if k == "plan" && v == "" {
				planIndent := itemIndent + 2
				pos++ // consume the "plan:" line
				plan, newPos, err := parsePlanBlock(lines, pos, planIndent, cl.lineNo)
				if err != nil {
					return providers, pos, err
				}
				pos = newPos
				prov.Plan = &plan
				continue
			}

			if err := setProviderField(&prov, k, v, cl.lineNo); err != nil {
				return providers, pos, err
			}
			pos++
		}

		providers = append(providers, prov)
	}

	return providers, pos, nil
}

// parsePlanBlock reads a nested "plan:" map at the given indentation. The
// parent "plan:" line has already been consumed; pos points at the first
// field line. Returns the PlanConfig and the index past the block.
func parsePlanBlock(lines []yamlLine, pos, indent, parentLineNo int) (PlanConfig, int, error) {
	var pc PlanConfig

	if pos >= len(lines) {
		return pc, pos, fmt.Errorf("line %d: 'plan:' block has no fields", parentLineNo)
	}
	if lines[pos].indent != indent {
		return pc, pos, fmt.Errorf("line %d: expected %d-space indentation for plan fields, got %d",
			lines[pos].lineNo, indent, lines[pos].indent)
	}

	for pos < len(lines) && lines[pos].indent == indent {
		ln := lines[pos]
		key, value, ok := splitKV(ln.content)
		if !ok {
			return pc, pos, fmt.Errorf("line %d: expected 'key: value' in plan block, got %q", ln.lineNo, ln.content)
		}

		switch key {
		case "cost":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return pc, pos, fmt.Errorf("line %d: plan cost must be a number, got %q", ln.lineNo, value)
			}
			pc.Cost = f
		case "currency":
			if value == "" {
				return pc, pos, fmt.Errorf("line %d: plan currency requires a value", ln.lineNo)
			}
			pc.Currency = unquote(value)
		case "cost_usd":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return pc, pos, fmt.Errorf("line %d: plan cost_usd must be a number, got %q", ln.lineNo, value)
			}
			pc.CostUSD = f
			pc.HasCostUSD = true
		case "label":
			if value == "" {
				return pc, pos, fmt.Errorf("line %d: plan label requires a value", ln.lineNo)
			}
			pc.Label = unquote(value)
		default:
			return pc, pos, fmt.Errorf("line %d: unknown plan field %q (known: cost, currency, cost_usd, label)", ln.lineNo, key)
		}
		pos++
	}

	return pc, pos, nil
}

// It enforces the schema: no literal "key" field, no sk-/gsk_ values,
// no unknown fields.
func setProviderField(p *YamlProvider, key, value string, lineNo int) error {
	switch key {
	case "id":
		p.ID = unquote(value)
	case "enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("line %d: enabled must be true or false, got %q", lineNo, value)
		}
		p.Enabled = b
	case "label":
		p.Label = unquote(value)
	case "key_env":
		envName := unquote(value)
		if strings.HasPrefix(envName, "sk-") || strings.HasPrefix(envName, "gsk_") {
			return fmt.Errorf("line %d: key_env must be an env var NAME, not a key value (starts with sk-/gsk_) — put the key in an env var", lineNo)
		}
		p.KeyEnv = envName
	case "probe":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("line %d: probe must be true or false, got %q", lineNo, value)
		}
		p.Probe = b
		p.HasProbe = true
	case "key":
		return fmt.Errorf("line %d: use 'key_env' to name the env var holding the key, not a literal 'key' field — the YAML must never contain a key value", lineNo)
	case "plan":
		return fmt.Errorf("line %d: 'plan:' must start a nested block (plan: %s is not valid)", lineNo, value)
	default:
		return fmt.Errorf("line %d: unknown provider field %q (known: id, enabled, label, key_env, probe, plan)", lineNo, key)
	}
	return nil
}
