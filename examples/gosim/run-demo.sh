#!/usr/bin/env bash
# Fetch and patch gosim, run the broadcast cluster and the checkout saga on
# it with gosim's own seeds (no qenv choices, no model), then run the tests
# that make every choice from qenv and check the runs against the models.
#   SEEDS=1-5     gosim's seeds for the first part
#   FULL=1        include the Hegel test (minutes: shrinking against the model)
set -euo pipefail
cd "$(dirname "$0")"
../../qgosim/fetch-gosim.sh
echo; echo "\$ gosim test -seeds=${SEEDS:-1-5} ./broadcast/cluster ./checkout"
go run github.com/jellevandenhooff/gosim/cmd/gosim test -seeds="${SEEDS:-1-5}" -v ./broadcast/cluster ./checkout 2>&1 | grep -v "unknown linkname"
echo; echo "\$ go test -v ./..."
short=-short
[ -n "${FULL:-}" ] && short=
go test $short -count=1 -v ./... 2>&1 | grep -E '^(=== RUN|--- |    host_test|ok|FAIL|PASS)'
