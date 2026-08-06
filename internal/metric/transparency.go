package metric

// Transparency is the salary-disclosure rate over a window: how many postings
// advertise a monthly salary out of how many exist at all.
//
// Every median in this package describes only the disclosed subset, so the
// pair travels with the number rather than being recomputed per page — two
// hand-rolled copies would drift the moment "disclosed" changes meaning.
type Transparency struct {
	Disclosed int
	Total     int
}

// Pct is the disclosed share, or 0 for an empty window (never NaN — this value
// is printed beside every salary figure).
func (t Transparency) Pct() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.Disclosed) / float64(t.Total)
}
