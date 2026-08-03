// Package store provides SQLite persistence for jobs-sg.
//
// Schema mirrors docs/03-data-model.md §2 (a living document).
// Evolution rule: archive-before-parse — the DB must always be rebuildable
// from raw/*.jsonl.gz, so schema changes are not scary.
package store

const schema = `
CREATE TABLE IF NOT EXISTS company (
  uen             TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  name_normalized TEXT NOT NULL,
  ssic_code       TEXT,
  employee_count  INTEGER,
  company_type    TEXT,
  first_seen_at   TEXT NOT NULL,
  last_seen_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS job (
  uuid                TEXT PRIMARY KEY,
  job_post_id         TEXT UNIQUE NOT NULL,
  title               TEXT NOT NULL,
  description_sha256  TEXT NOT NULL,
  source              TEXT NOT NULL DEFAULT 'mcf',
  canonical_fp        TEXT,
  company_uen         TEXT REFERENCES company(uen),
  ssoc_code           TEXT,
  occupation_id       TEXT,
  category            TEXT,
  position_level      TEXT,
  employment_type     TEXT,
  min_years_exp       INTEGER,
  salary_min          INTEGER,
  salary_max          INTEGER,
  salary_type         TEXT,
  salary_hidden       INTEGER NOT NULL DEFAULT 0,
  vacancies           INTEGER,
  role_family         TEXT,
  seniority           TEXT,
  work_mode           TEXT,
  is_swe              INTEGER NOT NULL DEFAULT 0,
  posting_date        TEXT NOT NULL,
  original_posting_date TEXT,
  expiry_date         TEXT,
  repost_count        INTEGER DEFAULT 0,
  status              TEXT,
  first_seen_at       TEXT NOT NULL,
  last_seen_at        TEXT NOT NULL,
  miss_count          INTEGER NOT NULL DEFAULT 0,
  closed_at           TEXT,
  view_count          INTEGER,
  application_count   INTEGER,
  district            TEXT,
  postal_code         TEXT,
  lat                 REAL,
  lng                 REAL,
  is_overseas         INTEGER DEFAULT 0,
  raw_path            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_job_posting_date ON job(posting_date);
CREATE INDEX IF NOT EXISTS idx_job_active       ON job(last_seen_at, closed_at);
CREATE INDEX IF NOT EXISTS idx_job_swe          ON job(is_swe, posting_date);
CREATE INDEX IF NOT EXISTS idx_job_company      ON job(company_uen);
CREATE INDEX IF NOT EXISTS idx_job_fp           ON job(canonical_fp);
-- The daily pages slice by crawl time, not posting time (web /daily), and
-- /metrics counts open vs closed on every scrape. Without these both are
-- full scans of an ~86k-row table on every request.
CREATE INDEX IF NOT EXISTS idx_job_first_seen   ON job(first_seen_at);
CREATE INDEX IF NOT EXISTS idx_job_closed       ON job(closed_at);

CREATE TABLE IF NOT EXISTS job_skill (
  job_uuid     TEXT NOT NULL REFERENCES job(uuid),
  skill        TEXT NOT NULL,
  is_key_skill INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (job_uuid, skill)
);

CREATE TABLE IF NOT EXISTS job_tech (
  job_uuid  TEXT NOT NULL REFERENCES job(uuid),
  tech_slug TEXT NOT NULL,
  tech_kind TEXT NOT NULL,
  source    TEXT NOT NULL,   -- rule | llm (both may coexist)
  PRIMARY KEY (job_uuid, tech_slug, source)
);

CREATE TABLE IF NOT EXISTS tech_taxonomy (
  alias TEXT PRIMARY KEY, tech_slug TEXT NOT NULL, tech_kind TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ssoc_taxonomy (
  ssoc_code   TEXT PRIMARY KEY,
  role_family TEXT NOT NULL,
  note        TEXT
);

CREATE TABLE IF NOT EXISTS job_source_xref (
  source      TEXT NOT NULL,
  source_id   TEXT NOT NULL,
  canon_uuid  TEXT NOT NULL REFERENCES job(uuid),
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  PRIMARY KEY (source, source_id)
);

CREATE TABLE IF NOT EXISTS job_repost (
  repost_uuid TEXT PRIMARY KEY REFERENCES job(uuid),
  canon_uuid  TEXT NOT NULL REFERENCES job(uuid),
  linked_at   TEXT NOT NULL,
  basis       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS unmapped_tech (
  raw_term      TEXT NOT NULL,
  first_seen_at TEXT NOT NULL,
  seen_count    INTEGER DEFAULT 1,
  reviewed      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (raw_term)
);

CREATE TABLE IF NOT EXISTS enrich_cache (
  description_sha256 TEXT NOT NULL,
  model              TEXT NOT NULL,
  prompt_version     TEXT NOT NULL,
  result_json        TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  PRIMARY KEY (description_sha256, model, prompt_version)
);

CREATE TABLE IF NOT EXISTS weekly_metric (
  week_start TEXT NOT NULL,
  metric     TEXT NOT NULL,
  dim_key    TEXT NOT NULL DEFAULT '',
  dim_type   TEXT NOT NULL DEFAULT '',
  value      REAL NOT NULL,
  computed_at TEXT NOT NULL,
  PRIMARY KEY (week_start, metric, dim_type, dim_key)
);

CREATE TABLE IF NOT EXISTS ingest_run (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  kind         TEXT NOT NULL,
  started_at   TEXT NOT NULL,
  ended_at     TEXT,
  pages_fetched INTEGER DEFAULT 0,
  jobs_seen    INTEGER DEFAULT 0,
  jobs_new     INTEGER DEFAULT 0,
  jobs_updated INTEGER DEFAULT 0,
  jobs_closed  INTEGER DEFAULT 0,
  llm_calls    INTEGER DEFAULT 0,
  llm_cached   INTEGER DEFAULT 0,
  errors       INTEGER DEFAULT 0,
  watermark    TEXT,
  status       TEXT NOT NULL
);
`

// techSeeds: alias -> (tech_slug, tech_kind). MVP seed; extend via
// unmapped_tech weekly review (docs/03 §7).
var techSeeds = [][3]string{
	// languages
	{"go", "go", "language"}, {"golang", "go", "language"},
	{"python", "python", "language"}, {"py", "python", "language"},
	{"javascript", "javascript", "language"}, {"js", "javascript", "language"},
	{"typescript", "typescript", "language"}, {"ts", "typescript", "language"},
	{"java", "java", "language"}, {"kotlin", "kotlin", "language"},
	{"swift", "swift", "language"}, {"rust", "rust", "language"},
	{"cpp", "cpp", "language"}, {"c++", "cpp", "language"},
	{"csharp", "csharp", "language"}, {"c#", "csharp", "language"},
	{"php", "php", "language"}, {"ruby", "ruby", "language"},
	{"scala", "scala", "language"}, {"sql", "sql", "language"},
	{"shell", "shell", "language"}, {"bash", "shell", "language"},
	{"html", "html", "language"}, {"css", "css", "language"},
	{"node", "nodejs", "language"}, {"nodejs", "nodejs", "language"},
	// frameworks
	{"react", "react", "framework"}, {"reactjs", "react", "framework"},
	{"vue", "vue", "framework"}, {"vuejs", "vue", "framework"},
	{"angular", "angular", "framework"}, {"angularjs", "angular", "framework"},
	{"nextjs", "nextjs", "framework"}, {"next.js", "nextjs", "framework"},
	{"spring", "spring", "framework"}, {"springboot", "spring", "framework"},
	{"django", "django", "framework"}, {"flask", "flask", "framework"},
	{"fastapi", "fastapi", "framework"}, {"express", "expressjs", "framework"},
	{"rails", "rails", "framework"},
	{"laravel", "laravel", "framework"}, {"dotnet", "dotnet", "framework"},
	{"spark", "spark", "framework"}, {"hadoop", "hadoop", "tool"},
	// cloud
	{"aws", "aws", "cloud"}, {"amazon web services", "aws", "cloud"},
	{"azure", "azure", "cloud"},
	{"gcp", "google-cloud", "cloud"}, {"google cloud", "google-cloud", "cloud"},
	{"aliyun", "aliyun", "cloud"}, {"alibaba cloud", "aliyun", "cloud"},
	{"cloudflare", "cloudflare", "cloud"},
	// databases
	{"postgresql", "postgresql", "database"}, {"postgres", "postgresql", "database"}, {"pg", "postgresql", "database"},
	{"mysql", "mysql", "database"},
	{"mongodb", "mongodb", "database"}, {"mongo", "mongodb", "database"},
	{"redis", "redis", "database"},
	{"elasticsearch", "elasticsearch", "database"}, {"elastic search", "elasticsearch", "database"},
	{"sqlite", "sqlite", "database"},
	{"cassandra", "cassandra", "database"}, {"clickhouse", "clickhouse", "database"},
	{"snowflake", "snowflake", "database"}, {"dynamodb", "dynamodb", "database"},
	{"bigquery", "bigquery", "database"}, {"mssql", "mssql", "database"},
	{"sql server", "mssql", "database"},
	// tools
	{"docker", "docker", "tool"}, {"kubernetes", "kubernetes", "tool"}, {"k8s", "kubernetes", "tool"},
	{"terraform", "terraform", "tool"}, {"ansible", "ansible", "tool"},
	{"jenkins", "jenkins", "tool"}, {"github actions", "github-actions", "tool"},
	{"gitlab ci", "gitlab-ci", "tool"}, {"git", "git", "tool"},
	{"prometheus", "prometheus", "tool"}, {"grafana", "grafana", "tool"},
	{"datadog", "datadog", "tool"}, {"newrelic", "newrelic", "tool"},
	{"kibana", "kibana", "tool"}, {"nginx", "nginx", "tool"},
	{"linux", "linux", "tool"}, {"grpc", "grpc", "tool"},
	{"rabbitmq", "rabbitmq", "tool"}, {"kafka", "kafka", "tool"},
	{"airflow", "airflow", "tool"}, {"dbt", "dbt", "tool"},
	// ai
	{"pytorch", "pytorch", "ai"}, {"tensorflow", "tensorflow", "ai"}, {"tf", "tensorflow", "ai"},
	{"llm", "llm", "ai"}, {"rag", "rag", "ai"},
	{"openai", "openai", "ai"}, {"chatgpt", "openai", "ai"},
	{"langchain", "langchain", "ai"}, {"llama", "llama", "ai"},
	{"huggingface", "huggingface", "ai"},
	{"generative ai", "generative-ai", "ai"}, {"genai", "generative-ai", "ai"},
	{"machine learning", "machine-learning", "ai"},
	{"deep learning", "deep-learning", "ai"},
	{"nlp", "nlp", "ai"}, {"computer vision", "computer-vision", "ai"},
	{"scikit-learn", "scikit-learn", "ai"}, {"sklearn", "scikit-learn", "ai"},
	{"xgboost", "xgboost", "ai"},
}

// ssocSeeds: ssoc_code -> (role_family, note). MVP seed aligned to ISCO-08
// groups from the 2026-08-02 site survey; MUST be re-verified against real
// sampling (Phase 0) before trusting weekly numbers (docs/02 §4.1, 05 Phase 0).
var ssocSeeds = [][3]string{
	{"25111", "Platform", "MVP seed: Systems Analyst (ISCO-08 2511)"},
	{"25112", "Platform", "MVP seed: Systems Analyst (ISCO-08 2511)"},
	{"25121", "Backend", "Verified 2026-08-02: Golang Developer"},
	{"25122", "Backend", "MVP seed: Software Developer (ISCO-08 2512)"},
	{"25131", "Frontend", "MVP seed: Web Developer (ISCO-08 2513)"},
	{"25132", "Mobile", "MVP seed: Mobile Developer (ISCO-08 2513)"},
	{"25141", "Backend", "MVP seed: Applications Programmer (ISCO-08 2514)"},
	{"25191", "Other-IT", "MVP seed: SW dev/analyst n.e.c."},
	{"25211", "Data", "MVP seed: Database Designer & Admin (ISCO-08 2521)"},
	{"25212", "Data", "MVP seed: Database Administrator (ISCO-08 2521)"},
	{"25221", "Platform", "MVP seed: Systems Administrator (ISCO-08 2522)"},
	{"25231", "Platform", "MVP seed: Network Professional (ISCO-08 2523)"},
	{"25291", "Platform", "MVP seed: DB/network professional n.e.c."},
	{"21221", "Data", "MVP seed: Data Scientist (ISCO-08 2122)"},
	{"21222", "Data", "Verified 2026-08-02: Data Engineer"},
	{"21223", "Data", "MVP seed: Data Analyst (ISCO-08 2122)"},
}
