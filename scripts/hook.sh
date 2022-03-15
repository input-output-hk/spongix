#!/bin/sh

set -euf

export IFS=' '

echo "Uploading paths to cache $OUT_PATHS"
exec nix copy --to 's3://cache?endpoint=127.0.0.1:7070&scheme=http' $OUT_PATHS
