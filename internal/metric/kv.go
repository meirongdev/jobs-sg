// Package metric computes every job-seeker statistic with SQL only — the LLM
// never produces a number (docs/01 §4). It renders nothing: HTML lives in
// internal/view, so the static weekly report and the live pages can share one
// aggregate layer.
package metric

// KV is a labeled value. It is the canonical type for chart input; report.KV
// is an alias of it so the weekly report keeps compiling unchanged.
type KV struct {
	Key   string
	Value float64
}
