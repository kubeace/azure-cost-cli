#!/usr/bin/env bash
# Generate a monthly Azure cost report.
#
# Output lands in ${REPORT_DIR:-./cost-reports}/azure_mtd_<YYYY-MM-DD>.md
#
# Usage:
#   ./monthly-report.sh                # this month
#   ./monthly-report.sh 2026-04        # specific month (first → last day)
#
# Env overrides:
#   REPORT_DIR     output directory       (default: ./cost-reports)
#   AZCOST_TENANT  Azure tenant override  (default: az login default)

set -euo pipefail

REPORT_DIR="${REPORT_DIR:-./cost-reports}"
TENANT_FLAG=()
if [ -n "${AZCOST_TENANT:-}" ]; then
  TENANT_FLAG=(--tenant "${AZCOST_TENANT}")
fi

if [ -n "${1:-}" ]; then
  YM="$1"
  FROM="${YM}-01"
  TO=$(date -u -d "${FROM} +1 month -1 day" +%F)
  STAMP="${YM}"
else
  FROM=$(date -u +%Y-%m-01)
  TO=$(date -u +%F)
  STAMP=$(date -u +%F)
fi

OUT="${REPORT_DIR}/azure_mtd_${STAMP}.md"
mkdir -p "${REPORT_DIR}"

echo "Generating ${OUT} for window ${FROM} → ${TO}..." >&2

azcost report "${TENANT_FLAG[@]}" --from "${FROM}" --to "${TO}" > "${OUT}"

# Optional: snapshot the same window for future diffs
azcost snapshot save "${TENANT_FLAG[@]}" --from "${FROM}" --to "${TO}" >&2

echo "Wrote ${OUT} ($(wc -l < "${OUT}") lines)" >&2
echo "${OUT}"
