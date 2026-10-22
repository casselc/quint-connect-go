#!/usr/bin/env bash
# Fetches gosim at the commit the fork is based on into .gosim-fork and
# applies gosim.patch (Go 1.26 support, choices through a Chooser). Modules
# using qgosim replace github.com/jellevandenhooff/gosim with that directory.
set -euo pipefail
cd "$(dirname "$0")"
BASE=ffd3a613542675755e4cbf8186b5edaf404ed95c
if [ -d .gosim-fork ]; then
  echo ".gosim-fork exists; remove it to fetch again" >&2
  exit 0
fi
git clone -q https://github.com/jellevandenhooff/gosim .gosim-fork
git -C .gosim-fork checkout -q "$BASE"
git -C .gosim-fork apply ../gosim.patch
echo "gosim fork ready in .gosim-fork"
