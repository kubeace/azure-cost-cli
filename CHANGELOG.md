# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] — initial open-source release

First public release. Extracted and generalised from an internal tool
that had been in production use for ~9 months.

### Subcommands
- `subs` — MTD cost per subscription, auto-discovers via `az account list`
- `services` / `rgs` / `resources` / `service-split` — sub-scoped breakdowns
- `cogsvc` — Cognitive Services meter split + per-deployment token / cache-rate from Azure Monitor
- `report` — full markdown report (subs + services + RGs + top resources + top-N service splits)
- `daily` — per-day breakdown with unicode sparkline
- `diff` — compare two windows; mover table sorted by absolute change
- `anomaly` — flag series whose latest day > ratio × rolling baseline
- `snapshot save` / `snapshot list` — JSON archive in `$XDG_CACHE_HOME/azcost/`
- `tags` / `tag-coverage` — local-YAML tag-map cost grouping + coverage report

### Auth
- `DefaultAzureCredential` chain by default — works with local `az login`
  with zero setup
- `--auth {auto,cli,sp}` flag for explicit mode selection
- `--auth sp` honours `--tenant` for cross-tenant SP queries
  (`ClientSecretCredential` with explicit authority override)

### Output
- Four formats: `table` (default), `md`, `csv`, `json`
- CSV uses a stable schema (`rank,label,extra,sub,inr,usd`) across all commands
- Currency display: `USD` (default, passthrough), `local`, or `both`
- `--rate` divisor for non-USD-billed EAs (INR, EUR, GBP, …)

### Reliability
- 429 / 503 retry with exponential backoff (5s → 2m, max 8 attempts)
- Per-attempt token refresh
- `Retry-After` header parsing (delta-seconds + HTTP-date)
- `--rps` throttle (default 5) shared across goroutines
- Parallel multi-sub fanout (per-sub scope = separate rate-limit bucket)

### Build
- Single static binary, CGO disabled
- `make install` → `~/.local/bin/azcost` (no sudo)
- Shell completion for bash / zsh / fish

[Unreleased]: https://github.com/kubeace/azure-cost-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/kubeace/azure-cost-cli/releases/tag/v0.1.0
