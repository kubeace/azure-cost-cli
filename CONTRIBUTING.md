# Contributing

Thanks for considering a contribution. This is a small, focused tool —
keep PRs equally small and focused.

## Quick start

```bash
git clone https://github.com/kubeace/azure-cost-cli
cd azure-cost-cli
make build
make test
```

Requirements: Go ≥ 1.25, an `az login` session for manual smoke tests.

## Before opening a PR

```bash
gofmt -l .                       # must produce no output
go vet ./...                     # must pass
go test -race -count=1 ./...     # must pass
```

Coverage target: ≥ 80% for new packages. Existing packages have their
current floor enforced by CI.

## Adding a new subcommand

1. Add a constructor in `cmd/azcost/` returning a `*cobra.Command`.
2. Wire it into the `root.AddCommand(...)` block in `main.go`.
3. Use `azure.Query()` / `azure.Metric()` from `internal/azure` rather
   than calling ARM directly.
4. Return `[]render.CostRow` and render via `render.Write(...)` so
   `--format` / `--top` / `--currency` work for free.
5. Add a row to the command-reference table in `README.md`.
6. Tests: pure logic (helpers, parsing) → unit tests in the matching
   package; cobra wiring is exercised by the existing fake-credential /
   RoundTripper harness in `internal/azure/azure_test.go`.

## Adding a new output format

`render.Format` is a string enum and `render.Write()` switches on it. To
add a new format:

1. Add the constant in `internal/render/render.go`.
2. Add a `case` in the `Write` switch with the renderer.
3. Add a table-driven test in `internal/render/render_test.go` covering
   at least: empty rows, single row, top-N truncation, currency=both vs USD.
4. Document in the README "Output formats" table.

## Commit conventions

Conventional commits. Types: `feat`, `fix`, `refactor`, `test`, `chore`,
`docs`, `ci`, `perf`. Scope is optional but encouraged:

```
feat(daily): add --group MeterCategory
fix(auth): honour --tenant for AzureCLICredential
docs(readme): clarify EA enrollment gotcha
```

Keep commit messages imperative, lowercase subject, no trailing period.

## Reporting issues

Please include:
- `azcost --version`
- The command you ran (redacted as needed)
- The full error message
- Whether the same command works under `az` directly (e.g. `az
  costmanagement query ...`) — this helps separate API issues from CLI
  bugs

For auth issues specifically, also include:
- Output of `az account show --query '{tenantId:tenantId,id:id}'`
- Which `--auth` mode you used (or unset = `auto`)

## License

By contributing, you agree your contribution is licensed under the same
[Apache-2.0](LICENSE) terms as the project.
