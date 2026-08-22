package detect

import (
	"fmt"
	"sort"
	"strings"
)

// AnalyseFeatures contrasts the candidates that contain a labelled ring with
// those that do not, so thresholds are chosen from measured separation instead
// of taste. A feature whose two distributions sit on top of each other carries
// no information and should not be in the score however plausible it sounds.
func AnalyseFeatures(led *Ledger, candidates []Candidate) string {
	ringTxn := make(map[int32]bool)
	for _, t := range led.Txns {
		if t.PatternID != 0 {
			ringTxn[t.ID] = true
		}
	}

	var withRing, without []Features
	for _, c := range candidates {
		hit := false
		for _, id := range c.TxnIDs {
			if ringTxn[id] {
				hit = true
				break
			}
		}
		if hit {
			withRing = append(withRing, c.Features)
		} else {
			without = append(without, c.Features)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "feature separation: %d candidates touching a labelled ring, %d not\n\n",
		len(withRing), len(without))
	fmt.Fprintf(&b, "%-18s %30s %30s %8s\n", "", "touching a ring", "ordinary", "")
	fmt.Fprintf(&b, "%-18s %9s %9s %9s %9s %9s %9s %8s\n",
		"feature", "p50", "p90", "p99", "p50", "p90", "p99", "lift@p50")

	type extractor struct {
		name string
		get  func(Features) float64
	}
	for _, e := range []extractor{
		{"txns", func(f Features) float64 { return float64(f.Txns) }},
		{"accounts", func(f Features) float64 { return float64(f.Accounts) }},
		{"total_amount", func(f Features) float64 { return f.TotalAmount }},
		{"max_amount", func(f Features) float64 { return f.MaxAmount }},
		{"mean_amount", func(f Features) float64 { return f.MeanAmount }},
		{"span_hours", func(f Features) float64 { return f.SpanHours }},
		{"pass_through", func(f Features) float64 { return f.PassThroughRatio }},
		{"conservation", func(f Features) float64 { return f.Conservation }},
		{"density", func(f Features) float64 { return f.Density }},
	} {
		a := collect(withRing, e.get)
		o := collect(without, e.get)
		lift := 0.0
		if pct(o, 0.5) != 0 {
			lift = pct(a, 0.5) / pct(o, 0.5)
		}
		fmt.Fprintf(&b, "%-18s %9.2f %9.2f %9.2f %9.2f %9.2f %9.2f %8.2fx\n",
			e.name,
			pct(a, 0.5), pct(a, 0.9), pct(a, 0.99),
			pct(o, 0.5), pct(o, 0.9), pct(o, 0.99),
			lift)
	}
	return b.String()
}

func collect(fs []Features, get func(Features) float64) []float64 {
	out := make([]float64, 0, len(fs))
	for _, f := range fs {
		out = append(out, get(f))
	}
	sort.Float64s(out)
	return out
}

func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}
