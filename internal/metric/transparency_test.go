package metric

import "testing"

func TestTransparencyPct(t *testing.T) {
	if got := (Transparency{Disclosed: 288, Total: 360}).Pct(); got != 0.8 {
		t.Errorf("Pct = %v, want 0.8", got)
	}
	// A window with no postings must read 0, not NaN: the page prints this
	// next to every salary figure, and NaN renders as "NaN%".
	if got := (Transparency{}).Pct(); got != 0 {
		t.Errorf("empty window Pct = %v, want 0", got)
	}
}
