#!/usr/bin/env bash
# The example's demo: the provider on kind with Crossplane (see kind/demo.sh
# for MUTATION, DROP, KILL). Without Docker, `go test ./...` runs the
# simulated environment.
exec "$(dirname "$0")/kind/demo.sh" "$@"
