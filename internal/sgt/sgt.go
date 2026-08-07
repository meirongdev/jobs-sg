// Package sgt holds the one timezone this system reports in.
//
// Every calendar bucket in jobs-sg is an SGT day, week or month while every
// timestamp is stored as UTC (docs/03 §2), so the conversion shows up in the
// ingest pipeline, the metric layer, the report renderer and two command
// entrypoints. It lived as five identical copies, which meant five copies of
// the reason below — the part actually worth keeping in one place.
//
// A leaf package with no imports of its own, so even internal/mcf can use it
// without inverting the dependency direction.
package sgt

import "time"

// Zone is Singapore time.
//
// FixedZone, not LoadLocation: the scratch runtime image carries no tzdata, so
// LoadLocation returns nil and time.In(nil) panics — which it did, on every run,
// until it was found. Singapore has been a fixed UTC+8 with no DST since 1982,
// so there is nothing a tz database would add.
var Zone = time.FixedZone("SGT", 8*3600)
