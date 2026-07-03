package rrc

// accept is the decision-theoretic acceptance test that replaced the flat
// EdgeThreshold cutoff (A4). A candidate is accepted when its expected value
// clears the marginal price of the tokens it costs:
//
//	E[value] = P·V_gain − (1−P)·V_harm ≥ μ · tokens
//
// Parameterize the value scale by the loss ratio r = V_harm/(V_gain+V_harm)
// (the one honest hand-set scalar — the precision stance). Dividing the
// inequality by (V_gain+V_harm) and writing it in terms of r gives:
//
//	P·(1−r) − (1−P)·r ≥ μ' · tokens        (μ' = μ/(V_gain+V_harm))
//	P − r ≥ μ' · tokens
//	P ≥ r + μ'·tokens
//
// So acceptance is P(prereq) ≥ r + μ·tokens, where μ is the budget's shadow
// price (0 when the budget is slack). At edge formation μ=0, so the floor is
// exactly the precision stance r; the token-scarcity tightening happens later
// in the assembly shed loop, where the budget actually binds.
//
// This is where abstention and fill-to-quality EMERGE rather than being
// asserted: slack budget → μ=0 → only candidates that genuinely clear the
// precision stance survive (a self-contained turn accepts few, or none);
// binding budget → μ rises → the floor self-tightens to the highest-P items.
func accept(p, lossRatio, mu float64, tokens int) bool {
	return p >= lossRatio+mu*float64(tokens)
}
