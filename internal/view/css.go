// Package view holds every HTML fragment the site renders: shared CSS, SVG
// chart components and page templates. It depends on internal/metric for data
// types and on nothing else, so the static weekly report and the live pages
// share one visual system instead of drifting apart in two template sets.
package view

// BaseCSS is shared by every page: the weekly report written to disk by
// cmd/report and the pages rendered live by internal/web.
const BaseCSS = `
:root{--bg:#0f172a;--card:#1e293b;--fg:#e2e8f0;--mut:#94a3b8;--acc:#2563eb}
*{box-sizing:border-box}body{margin:0;font:15px/1.55 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:var(--bg);color:var(--fg);padding:24px}
.wrap{max-width:900px;margin:0 auto}h1{font-size:26px;margin:0 0 4px}h2{font-size:19px;border-bottom:1px solid #334155;padding-bottom:6px;margin-top:32px}
.sub{color:var(--mut)}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:18px 0}
.card{background:var(--card);border-radius:10px;padding:14px}.card .n{font-size:26px;font-weight:700;color:#60a5fa}
.card .k{color:var(--mut);font-size:13px}table{width:100%;border-collapse:collapse;margin-top:10px}
td,th{padding:6px 8px;text-align:left;border-bottom:1px solid #334155}th{color:var(--mut);font-weight:500}
svg text{font-size:12px;fill:var(--fg)}svg .lab{fill:var(--mut)}.foot{margin-top:36px;color:var(--mut);font-size:12px}
svg.chart{width:100%;height:auto;max-width:100%}
.nav{margin:14px 0 4px;font-size:14px}.nav a{color:#60a5fa;text-decoration:none;margin-right:16px}
.nav a:hover{text-decoration:underline}.nav a.on{color:var(--fg);font-weight:600}
`

// SuppressedCSS styles the "we will not show you this number" states.
const SuppressedCSS = `
.mut{color:var(--mut)}.sup{color:var(--mut);font-variant-numeric:tabular-nums}
.up{color:#6ee7b7}.down{color:#fca5a5}
.lens{margin:10px 0 0;font-size:13px}.lens a{color:#60a5fa;text-decoration:none;margin-right:10px}
.lens a.on{color:var(--fg);font-weight:600}
.note{color:var(--mut);font-size:13px;margin:6px 0 0}
`
