# azcost

A small, fast Azure cost-management CLI. Works out-of-the-box with your
local `az login` session — no service principal, no env vars, no portal
setup required.

```bash
az login
go install github.com/kubeace/azure-cost-cli/cmd/azcost@latest

azcost subs                    # MTD bill across every enabled subscription
azcost services --sub <id>     # top services in one subscription
azcost daily    --sub <id>     # per-day trend with unicode sparkline
azcost anomaly  --sub <id>     # spike detector (>1.5× rolling baseline)
azcost report   > report.md    # full markdown report
```

Single static binary, ~14 MB. Wraps the Azure Cost Management `query` API
and Azure Monitor `metrics` API into 13 subcommands that produce
per-subscription / per-RG / per-resource / per-service / per-deployment
breakdowns, daily trends, diffs, anomaly detection, snapshots, and
client-side tag aggregation.

---

## Why

The official `az consumption` / `az costmanagement` commands return raw
JSON that you then `jq` into something usable. `azcost` does the
aggregation, formatting, and rate-limit handling for you, with
human-readable tables by default and CSV / JSON / Markdown when you need
them for pipelines.

Compared to [`mivano/azure-cost-cli`](https://github.com/mivano/azure-cost-cli)
(C#): different feature set — `azcost` adds per-day sparklines, an
anomaly detector, client-side tag-map grouping, a Cognitive Services
per-deployment token-share allocator, and snapshot save/diff. Static
single-file Go binary, no .NET runtime.

## Install

### From source (one command, requires Go ≥ 1.25)

```bash
go install github.com/kubeace/azure-cost-cli/cmd/azcost@latest
```

The binary lands in `$(go env GOBIN)` (defaults to `$(go env GOPATH)/bin`,
usually `~/go/bin`). Make sure that's on your `PATH`.

### From a clone (gives you `make install` → `~/.local/bin`)

```bash
git clone https://github.com/kubeace/azure-cost-cli
cd azure-cost-cli
make install        # → ~/.local/bin/azcost
azcost --version
```

### Pre-built releases

See [Releases](https://github.com/kubeace/azure-cost-cli/releases) for
pre-built binaries for Linux / macOS / Windows.

---

## Auth (the easy path)

**If you're running locally and have `az login` working, you're done.**
azcost uses the [Azure SDK `DefaultAzureCredential`](https://learn.microsoft.com/en-us/azure/developer/go/azure-sdk-authentication#defaultazurecredential)
chain, which falls through to your local `az` token when nothing else is
configured:

1. `EnvironmentCredential` — `AZURE_*` env vars (skipped if unset)
2. `WorkloadIdentityCredential` — federated tokens in pods (skipped on laptops)
3. `ManagedIdentityCredential` — MSI on Azure VMs (skipped elsewhere)
4. **`AzureCLICredential` — your `az login` session** ← what runs on a laptop

No further setup. Just run the command.

```bash
az login                              # if you haven't already
azcost subs                           # works
```

### Multi-tenant `az login`?

If your `az` session covers more than one Entra tenant (common for
contractors and consultants), the default-selected tenant may not be the
one that owns the subscription you want to query. Symptom:
`InvalidAuthenticationTokenTenant`.

Fix: pass `--tenant <tenant-id>` (or set `AZCOST_TENANT`), or run
`az account set --subscription <id>` to pick the right active sub before
calling azcost.

```bash
azcost subs --tenant 00000000-0000-0000-0000-000000000000
```

### Headless / CI / cron

Use a service principal via env vars and `--auth sp`:

```bash
export AZURE_TENANT_ID=...
export AZURE_CLIENT_ID=...
export AZURE_CLIENT_SECRET=...
azcost subs --auth sp
```

Or use OIDC workload identity via `azure/login@v2` in GitHub Actions —
see [`examples/ci-cost-gate.yml`](examples/ci-cost-gate.yml).

### `--auth` reference

| Mode | Picks | Use when |
|------|-------|----------|
| `auto` (default) | `DefaultAzureCredential` (env → MSI → `az login`) | Local laptop OR pod with workload identity. Just works. |
| `cli` | `AzureCLICredential` (your `az login` token only) | You have `AZURE_*` env vars loaded but want to bypass them this call |
| `sp` | `ClientSecretCredential` from `AZURE_*` env | Headless cron / CI; fail fast if SP env is missing |

For cross-tenant SP queries, **always pass `--tenant <foreign-tenant>`**
alongside `--auth sp` — the SP would otherwise issue a token against
`AZURE_TENANT_ID` (its home tenant) which a foreign sub rejects.

### Required Azure roles

The minimum scope for full functionality is, on each subscription:

- `Cost Management Reader` — for `subs`, `services`, `rgs`, `resources`, `service-split`, `report`, `daily`, `diff`, `anomaly`, `snapshot`, `tags`, `tag-coverage`
- `Monitoring Reader` — for `cogsvc` per-deployment metrics
- `Reader` — to enumerate resource IDs

Less-privileged setups: drop `Monitoring Reader` if you don't use `cogsvc`.

### EA enrollments

Some Enterprise Agreement enrollments restrict cost-data access for
service principals. If `--auth sp` returns `AccountCostDisabled`, you
need an enrollment admin to enable **"AO View Charges"** under
Cost Management + Billing → your billing scope → Policies. Local
`az login` (a user, not SP) is unaffected.

---

## Configuration

`azcost` reads `~/.config/azcost.yaml` if present. All keys are optional.

```yaml
# ~/.config/azcost.yaml — see examples/azcost.yaml for the full template
# tenant: 00000000-0000-0000-0000-000000000000   # multi-tenant disambiguation
rate: 1.0                                         # local→USD divisor (1.0 = passthrough)
currency: USD                                     # USD | local | both
rps: 5                                            # max ARM requests/sec
# subs:                                           # default --subs for `report` / `snapshot`
#   - 00000000-...
```

Env-var equivalents: `AZCOST_TENANT`, `AZCOST_RATE`, `AZCOST_RPS`,
`AZCOST_CURRENCY`, `AZCOST_FORMAT`, `AZCOST_TOP`, `AZCOST_AUTH`,
`AZCOST_TAGMAP`, `AZCOST_GROUP_TAG`.

**Precedence**: flag > env > config file > built-in default.

### Non-USD billing currencies (INR, EUR, GBP, …)

Cost Management returns numbers in your enrollment's billing currency.
By default azcost shows that raw value labeled "USD" (correct for
USD-billed enrollments). If you're billed in another currency and want a
USD column too, set the divisor:

```bash
azcost subs --rate 89.985 --currency both       # INR EA
azcost subs --rate 0.92   --currency both       # EUR EA → USD
```

---

## Quick start

```bash
azcost subs                                      # all enabled subs (MTD)
azcost services --sub <id> --top 10              # top services in one sub
azcost rgs      --sub <id> --top 10              # top resource groups
azcost resources --sub <id> --top 30             # top resources w/ type

azcost daily    --sub <id> --top 10              # sparkline trend per service
azcost diff     --sub <id>                       # MTD vs prior same-length window
azcost anomaly  --sub <id>                       # spike detector

azcost report > /tmp/azure-mtd-$(date +%F).md    # full markdown report
azcost snapshot save                             # archive current state to ~/.cache/azcost

azcost cogsvc --rid <cogsvc-arm-id>              # per-deployment token + cache% breakdown

azcost services --sub <id> --format csv > services.csv
azcost services --sub <id> --format json | jq '.[] | select(.inr > 50000)'
```

---

## Command reference

| Command | What it does |
|---|---|
| `subs` | MTD cost per subscription (auto-discovers via `az account list`) |
| `services` | MTD cost by service name for one subscription |
| `rgs` | Top resource groups for one subscription |
| `resources` | Top resources for one subscription (with resource type) |
| `service-split` | Per-resource cost split within a single service (debug "X is up 50%") |
| `cogsvc` | Meter + per-deployment tokens/cache% for one Cognitive Services account |
| `report` | Full markdown report (subs + services + RGs + top resources + service splits) |
| `daily` | Per-day cost breakdown with a unicode sparkline |
| `diff` | Compare costs between two windows; sorted mover table |
| `anomaly` | Spike detector (>ratio × rolling baseline; ignores tiny spends) |
| `snapshot save` / `snapshot list` | Archive current state to `~/.cache/azcost/` JSON |
| `tags` | Cross-sub MTD aggregation by tag value (using local `--tagmap` YAML) |
| `tag-coverage` | What % of spend is classified by the tag-map; shows largest gaps |

Run `azcost <command> --help` for per-command flags.

---

## Persistent flags

| Flag | Default | Env var | Meaning |
|---|---|---|---|
| `--sub <id>`            | — | `AZCOST_SUB` | Subscription ID (sub-scoped commands) |
| `--subs <id,...>`       | discovered | `AZCOST_SUBS` | For `report`/`subs`/`snapshot save` |
| `--tenant <id>`         | — | `AZCOST_TENANT` | Azure tenant ID |
| `--from YYYY-MM-DD`     | first of month UTC | `AZCOST_FROM` | Window start |
| `--to YYYY-MM-DD`       | today UTC | `AZCOST_TO` | Window end |
| `--rate <f>`            | 1.0 | `AZCOST_RATE` | local→USD divisor (1.0 = passthrough) |
| `--currency USD\|local\|both` | USD | `AZCOST_CURRENCY` | Display columns |
| `--format table\|md\|csv\|json` | table (md for `report`) | `AZCOST_FORMAT` | Output |
| `--top N`               | varies | `AZCOST_TOP` | Top-N rows |
| `--rps N`               | 5 | `AZCOST_RPS` | Max ARM requests/sec |
| `--tagmap <path>`       | `~/.config/azcost-tags.yaml` | `AZCOST_TAGMAP` | Local tag-map YAML; missing file is a no-op |
| `--group-tag <key>`     | — | `AZCOST_GROUP_TAG` | Re-aggregate output by tag value |
| `--auth auto\|cli\|sp`  | auto | `AZCOST_AUTH` | Credential type — see "Auth" |

---

## Tag-based grouping (client-side)

`azcost` ships a small YAML rule-engine that classifies resources by
substring/regex matches on their label — no Azure Policy or
portal-tagging round-trip required. Drop a file at
`~/.config/azcost-tags.yaml` (template:
[`examples/azcost-tags.yaml`](examples/azcost-tags.yaml)):

```yaml
rules:
  - match_regex: "aks-spot.*"
    tags: {team: platform, env: prod, tier: spot}
  - match_regex: "(openai|cogsvc|whisper)"
    tags: {team: ai}
defaults:
  env: prod
```

Match semantics: `match` is case-insensitive substring; `match_regex` is
anchored regex (case-insensitive). **First match wins.** Order specific
rules before broad ones.

```bash
azcost tags --key team             # MTD cost grouped by tag/team
azcost tag-coverage                # % of spend classified + largest gaps
azcost resources --sub <id> --group-tag team   # any per-resource view, regrouped
```

---

## Output formats

| Format | What it's for | Notes |
|---|---|---|
| `table` (default) | Human reading in a terminal | Unicode-safe |
| `md` | Pasting into a markdown report | Headers per command |
| `csv` | Spreadsheets / finance pipelines | **Stable schema**: `rank,label,extra,sub,inr,usd` for every command |
| `json` | Other tools | Includes `inr_per_usd` rate for downstream conversion |

`daily`, `diff`, `anomaly`, `cogsvc` use custom layouts (sparkline /
mover table / deployment table) and ignore `--format`.

> Note on column names: the JSON / CSV schema uses `inr` / `usd` for
> historical reasons. `inr` is the raw billing-currency value (whatever
> Azure returns for your enrollment); `usd` is `inr / rate`. With the
> default `--rate 1.0` they're equal.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `failed to acquire a token` | `az login` expired or never ran | `az login` |
| `InvalidAuthenticationTokenTenant` | Wrong tenant in multi-tenant `az login` | `--tenant <id>` or `az account set --subscription <id>` |
| `status 403 ... AuthorizationFailed` | Missing `Cost Management Reader` on the sub | Grant the role at sub scope |
| `status 403 ... AccountCostDisabled` | EA enrollment blocks SP cost reads | Either use `--auth cli` (your user token) or enable "AO View Charges" on the enrollment |
| `status 429: ... throttled` | Cost Mgmt is throttling | Lower `--rps` (default 5); azcost retries up to 8× with exponential backoff |
| Zero rows in `service-split` | `--service` name case-mismatch | Run `azcost services --sub ...` to see the canonical capitalization |
| `no deployment-level metrics` from `cogsvc` | Resource isn't a Cognitive Services account, or no token usage in window | `az cognitiveservices account show` to verify; widen `--from`/`--to` |
| Numbers don't match Azure portal | (a) currency: portal may show USD, azcost shows billing currency; (b) window: portal MTD may use different timezone | Use `--currency both --rate <X>` for clarity; portal cost-analysis uses UTC by default |

---

## Layout

```
azure-cost-cli/
├── cmd/azcost/                  # cobra root + subcommand wiring
│   ├── main.go                  # subs/services/rgs/resources/service-split/cogsvc/report
│   ├── tags.go                  # tags + tag-coverage
│   └── trends.go                # daily + diff + anomaly + snapshot
├── internal/azure/              # Cost Management + Monitor client
│   ├── auth.go                  # DefaultAzureCredential, 429 retry, per-attempt token refresh
│   ├── costmgmt.go              # Query() with nextLink pagination + filter composition
│   ├── monitor.go               # Metric() for cogsvc deployment splits
│   └── azure_test.go            # fake-cred + RoundTripper unit tests
├── internal/render/             # table / md / csv / json + unicode sparkline
├── internal/trends/             # rolling avg, anomaly, diff (pure analytics)
├── internal/snapshot/           # ~/.cache/azcost JSON store
├── internal/tagmap/             # YAML tag-map engine
├── internal/config/             # viper config + AZCOST_* env vars
└── examples/                    # config + tag-map + CI workflow + monthly-report
```

---

## Develop

```bash
git clone https://github.com/kubeace/azure-cost-cli
cd azure-cost-cli

make build                       # → ./azcost
make test                        # go test -race -count=1 ./...
make install                     # → ~/.local/bin/azcost

go test -race -cover ./...       # coverage report
gofmt -l . && go vet ./...       # CI checks
```

Coverage today (target: 80%+):
- `internal/render` ~92%, `internal/trends` ~93%, `internal/snapshot` ~66%, `internal/tagmap` ~85%, `internal/azure` ~60%.

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to add a new subcommand
or output format.

---

## License

Apache-2.0. See [LICENSE](LICENSE).

This project is not affiliated with Microsoft. Azure, Azure Cost
Management, and related marks are trademarks of Microsoft Corporation.
