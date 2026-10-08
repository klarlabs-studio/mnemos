#!/usr/bin/env bash
# Scale gate: scalebench at 100k beliefs over the uniform, hub and
# contradiction corpora, each with three warm rounds, checked against the
# ceilings in bench/scale-gate.json. About ten minutes. A release-qualification
# step (#382 Phase 7), not a pre-push one. The 1M-belief runs that motivated it
# swing 2-4x with the OS page cache and take ~25 minutes each, so they stay a
# manual measurement (docs/consolidation/baseline/README.md).
set -euo pipefail
out="${SCALE_GATE_OUT:-$(mktemp -d)}"
mkdir -p "$out"
status=0
for shape in uniform hub contradiction; do
  echo "== scale gate: ${shape}"
  if ! go run ./tools/scalebench -beliefs 100000 -shape "$shape" -queries 30 -ingests 10 \
      -repeat 3 -timeout 10m -json "${out}/scale-gate-${shape}.json" -check bench/scale-gate.json; then
    status=1
  fi
done
echo "reports: ${out}"
exit "$status"
