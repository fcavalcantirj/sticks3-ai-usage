package stats

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
