#!/usr/bin/env bash
# Fetches the gosim fork into .gosim-fork: the gosim-fork branch of this
# repository (upstream gosim plus quint-connect-go's commits; its FORK.md
# says what they change), at the commit this checkout of qgosim was made
# against. Modules using qgosim replace github.com/jellevandenhooff/gosim
# with that directory.
#   GOSIM_REPO   where to clone from (default: this repository's origin)
set -euo pipefail
cd "$(dirname "$0")"
REF=70a175de9e7eff85a33c45ac3fb2f0f33c6defcf
if [ -d .gosim-fork ]; then
  # already there (CI checks the branch out itself): move it to REF
  if [ "$(git -C .gosim-fork rev-parse HEAD 2>/dev/null)" != "$REF" ] &&
    ! git -C .gosim-fork checkout -q "$REF" 2>/dev/null; then
    echo ".gosim-fork does not have $REF: remove it to fetch again" >&2
    exit 1
  fi
  exit 0
fi
REPO=${GOSIM_REPO:-$(git -C .. remote get-url origin)}
git clone -q --branch gosim-fork --single-branch "$REPO" .gosim-fork
git -C .gosim-fork checkout -q "$REF"
echo "gosim fork ready in .gosim-fork ($REF)"
