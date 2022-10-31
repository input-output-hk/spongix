#!/bin/sh

set -uf
export IFS=' '
for path in $DRV_PATH $OUT_PATHS; do
  nix copy --to 'http://alpha.fritz.box:7777/?compression=none' "$path"
done
