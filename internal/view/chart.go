package view

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// TopN returns at most n leading entries.
func TopN(kvs []metric.KV, n int) []metric.KV {
	if len(kvs) > n {
		return kvs[:n]
	}
	return kvs
}

// Bar draws a horizontal bar chart of up to maxBars entries, labelling values
// as plain counts.
func Bar(kvs []metric.KV, maxBars int) template.HTML {
	return bar(kvs, maxBars, "bar chart", func(v float64) string { return strconv.Itoa(int(v)) })
}

// BarMoney is Bar for monthly salaries. Bar's bare integer labels are right for
// counts, but a "8700" sitting above a table that reads "S$8,700" invites the
// reader to think the two are different measurements — the mislabeled-unit
// failure this project has already shipped once.
func BarMoney(kvs []metric.KV, maxBars int) template.HTML {
	return bar(kvs, maxBars, "monthly salary", Money)
}

// bar is the shared rendering behind Bar and BarMoney: a horizontal bar chart
// of up to maxBars entries, naming its unit in the aria-label and formatting
// each value with label.
func bar(kvs []metric.KV, maxBars int, unit string, label func(float64) string) template.HTML {
	if len(kvs) == 0 {
		return template.HTML("")
	}
	kvs = TopN(kvs, maxBars)
	max := 0.0
	for _, kv := range kvs {
		if kv.Value > max {
			max = kv.Value
		}
	}
	if max == 0 {
		max = 1
	}
	const barH = 22
	const gap = 6
	// Height follows the bar count — a fixed viewBox clipped everything past
	// the 11th bar while callers ask for 15. max-width pins the chart to its
	// viewBox width; stretched to the full column it scales the 12px type up.
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg viewBox="0 0 520 %d" style="max-width:520px" xmlns="http://www.w3.org/2000/svg" class="chart" role="img" aria-label="%s">`,
		10+len(kvs)*(barH+gap), unit))
	y := 10
	for _, kv := range kvs {
		// 340 not 400: the value label sits after the bar and rendered past the
		// right edge of the viewBox on the longest bar.
		w := 4 + int(340*(kv.Value/max))
		b.WriteString(fmt.Sprintf(`<text x="2" y="%d" class="lab">%s</text>`, y+barH-6, template.HTMLEscapeString(kv.Key)))
		b.WriteString(fmt.Sprintf(`<rect x="120" y="%d" width="%d" height="%d" rx="2" fill="#2563eb"/>`, y, w, barH))
		b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" class="val">%s</text>`, 126+w, y+barH-6, label(kv.Value)))
		y += barH + gap
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// chartScale picks the y-axis maximum, ignoring a lone outlier.
//
// The first-run baseline scan stores the entire live market (~86k postings) on
// a single day, so scaling to the true maximum renders every ordinary day as a
// 1px stub for the next 30 days. When the top value dwarfs the runner-up, the
// axis follows the runner-up and the outlier column is drawn clipped.
func chartScale(kvs []metric.KV) float64 {
	top, second := 0.0, 0.0
	for _, kv := range kvs {
		switch {
		case kv.Value > top:
			top, second = kv.Value, top
		case kv.Value > second:
			second = kv.Value
		}
	}
	if second > 0 && top > 3*second {
		return second
	}
	if top == 0 {
		return 1
	}
	return top
}

// Column draws a time-series column chart (dates left to right). Bar is
// horizontal and caps at ~11 rows, which cannot show a 30-day trend.
func Column(kvs []metric.KV, unit string) template.HTML {
	if len(kvs) == 0 {
		return template.HTML(`<p class="mut">No data yet.</p>`)
	}
	const (
		plotH   = 120
		baseY   = 140
		leftPad = 34
	)
	// Widen the columns when there are few days so a week of history is not
	// drawn as a 160px sliver, and keep them legible for a 90-day window.
	step := 700 / len(kvs)
	step = min(max(step, 17), 48)
	width := leftPad + len(kvs)*step + 10
	scale := chartScale(kvs)
	labelEvery := max((len(kvs)+7)/8, 1)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d 170" style="max-width:%dpx" xmlns="http://www.w3.org/2000/svg" class="chart" role="img" aria-label="%s per period">`,
		width, width, template.HTMLEscapeString(unit))
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#334155"/>`, leftPad-4, baseY, width-6, baseY)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#334155" stroke-dasharray="2 3"/>`,
		leftPad-4, baseY-plotH, width-6, baseY-plotH)
	fmt.Fprintf(&b, `<text x="0" y="%d" class="lab">%d</text>`, baseY-plotH+4, int(scale))
	fmt.Fprintf(&b, `<text x="0" y="%d" class="lab">0</text>`, baseY+4)

	for i, kv := range kvs {
		x := leftPad + i*step
		h := int(float64(plotH) * (kv.Value / scale))
		if h < 1 && kv.Value > 0 {
			h = 1
		}
		// A column past the scale is drawn clipped, in a lighter fill, with its
		// real value written above it.
		fill, clipped := "#2563eb", kv.Value > scale
		if clipped {
			h, fill = plotH, "#7c3aed"
		}
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="1" fill="%s"><title>%s: %d</title></rect>`,
			x, baseY-h, step-5, h, fill, template.HTMLEscapeString(kv.Key), int(kv.Value))
		if clipped {
			fmt.Fprintf(&b, `<text x="%d" y="%d" class="lab" text-anchor="middle" font-size="10">%d</text>`,
				x+(step-5)/2, baseY-plotH-4, int(kv.Value))
		}
		if (i%labelEvery == 0 && len(kvs)-1-i >= labelEvery/2) || i == len(kvs)-1 {
			fmt.Fprintf(&b, `<text x="%d" y="%d" class="lab" text-anchor="middle" font-size="10">%s</text>`,
				x+(step-5)/2, baseY+16, template.HTMLEscapeString(kv.Key))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
