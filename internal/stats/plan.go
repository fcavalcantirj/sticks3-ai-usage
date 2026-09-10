package stats

import "math"

// rateTolerance is the maximum fractional deviation allowed between plan-pair
// exchange rates before conversion is suppressed (1%).
const rateTolerance = 0.01

// ApplyPlanValues augments each source in the report with a PlanValue,
// computed from the API-equivalent month cost and the plan configuration
// supplied by the caller (from the YAML config). Sources without a plan entry
// in the plans map are left unchanged.
//
// The plans map is keyed by source name ("claude_code", "codex") to match the
// keys in Report.Sources. The caller is responsible for mapping provider IDs
// to source names (e.g. "claude" -> "claude_code").
func ApplyPlanValues(report *Report, plans map[string]PlanParams) {
	if report == nil {
		return
	}
	for srcName, src := range report.Sources {
		plan, ok := plans[srcName]
		if !ok || plan.Cost == 0 {
			continue
		}
		pv := computePlanValue(src.Month.Cost, plan, src.Partial)
		src.PlanValue = &pv
		report.Sources[srcName] = src
	}
}

// computePlanValue computes the PlanValue for a source given its month cost
// and plan config. The ratio (API-equiv / plan cost) is only computed when a
// USD cost is available — either the plan is already in USD, or cost_usd was
// manually supplied. A ratio below 1.0 flags Warn so the UI can show it in
// the warn color.
//
// A plan in a non-USD currency without cost_usd yields HasRatio=false: the
// dashboard shows the plan cost in its currency and no ratio (never an
// invented conversion).
func computePlanValue(monthCost float64, plan PlanParams, partial bool) PlanValue {
	pv := PlanValue{
		Cost:     plan.Cost,
		Currency: plan.Currency,
		CostUSD:  plan.CostUSD,
		Label:    plan.Label,
	}

	// Determine the USD plan cost: if the plan is in USD, use Cost directly;
	// otherwise use the manually-supplied cost_usd.
	usdCost := plan.CostUSD
	if plan.Currency == "USD" {
		usdCost = plan.Cost
	}

	if usdCost > 0 {
		pv.HasRatio = true
		if usdCost > 0 {
			pv.Ratio = monthCost / usdCost
		}
		if pv.Ratio < 1.0 {
			pv.Warn = true
		}
	}

	return pv
}

// ComputePlanCost builds a PlanCost from the plan parameters (all plan
// providers, not just scanned sources) and the stats report (for API-equivalent
// costs and partial flags). The primary currency is the one most plan providers
// use; the secondary currency (if any) is converted using a rate derived from
// plan pairs that carry both a local cost and a manual cost_usd.
//
// Rate derivation: every plan in the primary currency with a non-USD cost and
// a non-zero cost_usd yields rate = cost / cost_usd (primary-per-USD). All such
// rates must agree within rateTolerance; if they disagree, conversion is
// suppressed, RateDisagrees is set, and the offending plans are listed in
// OmittedPlans.
func ComputePlanCost(plans map[string]PlanParams, report *Report) *PlanCost {
	if len(plans) == 0 {
		return nil
	}

	// Determine the primary currency: the one used by the most plans
	// (with non-zero cost). Ties are broken by map iteration order (stable
	// for a given map, but we only rely on count, not order).
	currencyCount := map[string]int{}
	for _, p := range plans {
		if p.Cost == 0 {
			continue
		}
		currencyCount[p.Currency]++
	}
	if len(currencyCount) == 0 {
		return nil
	}

	primaryCurrency := ""
	for cur, count := range currencyCount {
		if primaryCurrency == "" || count > currencyCount[primaryCurrency] {
			primaryCurrency = cur
		}
	}

	// Secondary currency: any other currency that appears (at most one for
	// the USD+BRL use case).
	secondaryCurrency := ""
	for cur := range currencyCount {
		if cur != primaryCurrency {
			secondaryCurrency = cur
			break
		}
	}

	// Derive exchange rate from primary-currency plans with both cost and
	// cost_usd. rate = primary_per_USD (e.g. 5.5 BRL per USD).
	var rate float64
	rateSet := false
	rateDisagrees := false
	var omittedPlans []string

	if secondaryCurrency == "USD" {
		for _, p := range plans {
			if p.Cost == 0 || p.Currency != primaryCurrency || !p.HasCostUSD || p.CostUSD == 0 {
				continue
			}
			pairRate := p.Cost / p.CostUSD
			if !rateSet {
				rate = pairRate
				rateSet = true
			} else if math.Abs(pairRate-rate)/rate > rateTolerance {
				rateDisagrees = true
				omittedPlans = append(omittedPlans, p.Label)
			}
		}
	}

	canConvert := rateSet && !rateDisagrees && secondaryCurrency == "USD"

	// If there's a secondary currency but no rate, suppress conversion and
	// note the plans that can't be converted.
	if secondaryCurrency != "" && !canConvert && !rateDisagrees {
		rateDisagrees = true
		for _, p := range plans {
			if p.Cost == 0 || p.Currency != secondaryCurrency {
				continue
			}
			omittedPlans = append(omittedPlans, p.Label)
		}
	}

	// Compute totals: sum all plan costs in their native currencies, then
	// cross-convert using the derived rate when possible.
	primaryTotal := 0.0
	secondaryTotal := 0.0

	for _, p := range plans {
		if p.Cost == 0 {
			continue
		}
		switch p.Currency {
		case primaryCurrency:
			primaryTotal += p.Cost
			if canConvert {
				secondaryTotal += p.Cost / rate // primary → USD
			}
		case secondaryCurrency:
			secondaryTotal += p.Cost
			if canConvert {
				primaryTotal += p.Cost * rate // USD → primary
			}
		}
	}

	// API-equivalent costs from scanned sources (today vs month).
	apiCostToday := 0.0
	apiCostMonth := 0.0
	var apiUnpricedModels []string
	apiPartial := false
	if report != nil {
		for _, src := range report.Sources {
			apiCostToday += src.Today.Cost
			apiCostMonth += src.Month.Cost
			if src.Partial {
				apiPartial = true
				apiUnpricedModels = append(apiUnpricedModels, src.UnpricedModels...)
			}
		}
	}

	pc := &PlanCost{
		PrimaryCurrency:   primaryCurrency,
		PrimaryTotal:      primaryTotal,
		APICostToday:      apiCostToday,
		APICostMonth:      apiCostMonth,
		APIPartial:        apiPartial,
		APIUnpricedModels: apiUnpricedModels,
	}

	if canConvert {
		pc.SecondaryCurrency = secondaryCurrency
		pc.SecondaryTotal = secondaryTotal
		pc.ExchangeRate = rate
	}

	if rateDisagrees {
		pc.RateDisagrees = true
		pc.OmittedPlans = omittedPlans
	}

	return pc
}
