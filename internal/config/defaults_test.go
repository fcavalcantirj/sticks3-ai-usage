package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPlanPresets: the published list prices are the same for everyone, so
// nobody should have to type them. The picker offers them; the numbers here
// come from anthropic.com and openai.com/business/pricing (BRL), read
// 2026-09-07.
func TestPlanPresets(t *testing.T) {
	byProvider := PlanPresets()

	claude := byProvider[ProviderClaude]
	if len(claude) == 0 {
		t.Fatal("no Claude plan presets")
	}
	want := map[string]struct {
		cost    float64
		costUSD float64
	}{
		"Pro":     {110, 20},
		"Max 5x":  {550, 100},
		"Max 20x": {1100, 200},
	}
	seen := map[string]bool{}
	for _, p := range claude {
		w, ok := want[p.Label]
		if !ok {
			continue
		}
		seen[p.Label] = true
		if p.Cost != w.cost {
			t.Errorf("Claude %s cost = %v, want %v BRL", p.Label, p.Cost, w.cost)
		}
		if p.Currency != "BRL" {
			t.Errorf("Claude %s currency = %q, want BRL", p.Label, p.Currency)
		}
		if !p.HasCostUSD || p.CostUSD != w.costUSD {
			t.Errorf("Claude %s cost_usd = %v (set=%v), want %v", p.Label, p.CostUSD, p.HasCostUSD, w.costUSD)
		}
	}
	for l := range want {
		if !seen[l] {
			t.Errorf("Claude preset %q missing", l)
		}
	}

	// Max 20x is exactly double Max 5x — the pricing page says "choose 5x or
	// 20x" from one "From R$550" figure, so this relationship is the check
	// that the table was not mistyped.
	var m5, m20 float64
	for _, p := range claude {
		if p.Label == "Max 5x" {
			m5 = p.Cost
		}
		if p.Label == "Max 20x" {
			m20 = p.Cost
		}
	}
	if m20 != m5*2 {
		t.Errorf("Max 20x (%v) should be double Max 5x (%v)", m20, m5)
	}

	if len(byProvider[ProviderCodex]) == 0 {
		t.Error("no ChatGPT/Codex plan presets")
	}

	// OpenCode Go: $10/month, USD (Felipe confirmed 2026-09-09).
	og := byProvider[ProviderOpenCodeGo]
	if len(og) != 1 {
		t.Fatalf("OpenCode Go presets: got %d entries, want 1", len(og))
	}
	if og[0].Cost != 10 || og[0].Currency != "USD" || og[0].CostUSD != 10 ||
		!og[0].HasCostUSD || og[0].Label != "Go" {
		t.Errorf("OpenCode Go preset = %+v, want Cost=10 Currency=USD CostUSD=10 HasCostUSD=true Label=Go", og[0])
	}
}

// TestPlanPresetsCoverTheDefaults: every built-in default plan must appear in
// the picker, or the settings page opens showing a plan the dropdown cannot
// reproduce.
func TestPlanPresetsCoverTheDefaults(t *testing.T) {
	presets := PlanPresets()
	for _, d := range DefaultProviders() {
		if d.Plan == nil {
			continue
		}
		found := false
		for _, p := range presets[d.ID] {
			if p.Label == d.Plan.Label && p.Cost == d.Plan.Cost && p.Currency == d.Plan.Currency {
				found = true
			}
		}
		if !found {
			t.Errorf("provider %s default plan %q (%v %s) is not in the presets",
				d.ID, d.Plan.Label, d.Plan.Cost, d.Plan.Currency)
		}
	}
}

// TestPlanProvidersHaveDefaultPlan is the guard that stops task 100 from
// recurring: every provider whose fetcher declares Kind: "plan" MUST carry a
// non-nil Plan in DefaultProviders(). Instead of hardcoding the set of plan
// providers (which is the second-spelling bug that let opencode:go ship with a
// blank cost card), this test parses internal/providers/*.go with go/parser
// and go/ast to discover any composite literal that sets Kind: "plan", so a
// seventh plan provider is caught immediately with no list to update.
//
// CAVEAT: a provider that COMPUTES its kind at runtime (e.g. groq.go derives
// Kind from a variable, never "plan") is invisible to a source scan. If that
// ever changes, the fetcher should expose a static kind instead.
func TestPlanProvidersHaveDefaultPlan(t *testing.T) {
	planIDs, err := discoverPlanProviderIDs()
	if err != nil {
		t.Fatalf("scanning internal/providers for Kind: \"plan\": %v", err)
	}
	if len(planIDs) == 0 {
		t.Fatal("no plan providers found in internal/providers/ — the source scan returned empty")
	}

	for _, d := range DefaultProviders() {
		if planIDs[d.ID] {
			if d.Plan == nil {
				t.Errorf("plan provider %q (Kind: plan in internal/providers/) has no Plan in DefaultProviders()", d.ID)
			}
		} else {
			if d.Plan != nil {
				t.Errorf("non-plan provider %q has an unexpected Plan in DefaultProviders()", d.ID)
			}
		}
	}
}

// discoverPlanProviderIDs parses the Go source files in internal/providers/
// (excluding _test.go files) and returns the IDs of all providers whose
// fetcher sets Kind: "plan" as a string literal in a struct literal. The ID
// field may be a string literal or a const identifier (e.g. claudeID = "claude");
// const declarations in the same file are resolved to their string values.
func discoverPlanProviderIDs() (map[string]bool, error) {
	ids := map[string]bool{}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("cannot determine test file path via runtime.Caller")
	}
	providersDir := filepath.Join(filepath.Dir(filename), "..", "providers")

	fset := token.NewFileSet()

	entries, err := os.ReadDir(providersDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", providersDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(providersDir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		// Collect constant declarations: name -> unquoted string value.
		consts := map[string]string{}
		ast.Inspect(file, func(n ast.Node) bool {
			gen, ok := n.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Values == nil || len(vs.Values) == 0 {
					continue
				}
				lit, ok := vs.Values[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				for _, name := range vs.Names {
					consts[name.Name] = strings.Trim(lit.Value, "\"")
				}
			}
			return true
		})

		// Find composite literals with Kind: "plan" and extract their ID.
		ast.Inspect(file, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			kindPlan := false
			var idExpr ast.Expr
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				if key.Name == "Kind" {
					lit, ok := kv.Value.(*ast.BasicLit)
					if ok && lit.Kind == token.STRING && strings.Trim(lit.Value, "\"") == "plan" {
						kindPlan = true
					}
				}
				if key.Name == "ID" {
					idExpr = kv.Value
				}
			}
			if kindPlan && idExpr != nil {
				switch v := idExpr.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						ids[strings.Trim(v.Value, "\"")] = true
					}
				case *ast.Ident:
					if val, ok := consts[v.Name]; ok {
						ids[val] = true
					}
				}
			}
			return true
		})
	}

	return ids, nil
}
