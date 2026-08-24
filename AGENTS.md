# Repository Guidelines

jobs-sg is a Go 1.26 service that ingests Singapore SWE job postings from the public MyCareersFuture API into SQLite, enriches them with a local LLM, and publishes a weekly trend report. Targets a k3s homelab with ≤64 MiB memory and zero external cost.

## Project Structure & Module Organization

- `cmd/{ingest,enrich,report,web}` — command entrypoints; keep mains thin, put logic in `internal/`.
- `internal/{mcf,store,classify,tech,llm,ingest,metric,report,sgt,view,web}` — shared libraries per stage (`metric` = site+report aggregate layer, `view` = shared visual layer).
- `testdata/fixture/jobs.jsonl` — 360 fixtures across 6 complete ISO weeks (incl. closed postings and `min_years_exp` NULL) for replay tests; regenerate with `scripts/genfixture/`.
- `docs/` — numbered design docs (01–09); `03-data-model.md`, `05-roadmap.md`, and `08-bdd.md` are living docs kept in sync with code.
- `deploy/` — reference kustomize manifests; sync to `meirongdev/homelab` for ArgoCD (never point ArgoCD at this repo).

## Build, Test, and Development Commands

```sh
make build   # build bin/jobs-sg-{ingest,enrich,report,web}
make test    # run go test ./...
make vet     # run go vet ./...
make fmt     # gofmt
make tidy    # tidy go modules
go run ./scripts/genfixture  # regenerate fixtures deterministically
go run ./scripts/taxonomyaudit --data-dir ./data  # SSOC/tech taxonomy coverage report
go run ./scripts/reclassify --data-dir ./data     # replay the classify layer over raw/ (dry run; --apply to write)
go run ./scripts/retech --data-dir ./data         # replay the rule-layer tech extraction over raw/ (dry run; --apply to write)
```

Local smoke (needs MCF API access):

```sh
./bin/jobs-sg-ingest --data-dir ./data --delay-ms 1500
./bin/jobs-sg-web --data-dir ./data --addr :8080
```

## Coding Style & Naming Conventions

- Use `gofmt`; keep `go vet` clean (no external linter).
- Idiomatic Go: PascalCase exported identifiers, camelCase locals, uppercase acronyms (`SSOC`, `MCF`, `SWE`).
- Package/file doc comments plus exported-symbol docs; `cmd/` entrypoints stay thin with logic in `internal/` constructors.

## Testing Guidelines

- Standard `testing` package; `*_test.go` files colocated with the package.
- Name tests `Test<Subject><Behavior>` (e.g. `TestSSOCPrimaryHit`); use `t.Helper()` and `want`/`got` in failures.
- Prefer table-driven tests and fixture replay against `testdata/fixture/jobs.jsonl`.
- CI runs `go vet ./...` and `go test ./...` on every push/PR; no coverage threshold enforced.

## Commit & Pull Request Guidelines

- Conventional Commits: `feat:`, `docs:`, `chore:`, `fix:` with a short imperative subject (e.g. `feat: containerization + CI + deploy manifests`); explain the "why" when not obvious.
- Before opening a PR, run `make test` and `make vet`; CI must be green.
- Describe what changed and why, and reference the relevant `docs/` spec or roadmap DoD item. `deploy/` changes are reference-only — update the homelab repo for live effects.

## Security & Configuration

- Never commit secrets; configure them via Vault → ExternalSecret (see `docs/04-operations.md` §5).
- Respect the compliance red line (`docs/01-requirements.md` §5): transparent UA, public data only, no personal fields, aggregate-only reports.
- Build multi-arch, scratch-based, non-root images with pinned digests (Kyverno rejects non-digest tags).
