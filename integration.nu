#!/usr/bin/env nu

let cache = 'http://127.0.0.1:8888/public?compression=zstd'
let pubkey = (open ./public.key)

let-env PAGER = ""
let-env NIX_CONFIG = $'
substituters = ($cache)
tarball-ttl = 0
narinfo-cache-negative-ttl = 0
narinfo-cache-positive-ttl = 0
trusted-public-keys = ($pubkey)
'

rm -rf local

nix -vvvvvvvv build '.#testDerivation'
nix copy --to $cache '.#testDerivation'
# nix copy --derivation --to $cache '.#testDerivation'
# nix store copy-log --to $cache '.#testDerivation'
# nix log --store $cache '.#testDerivation'
# nix copy --no-update-lock-file --derivation --from $cache '.#testDerivation'
# nix copy --no-update-lock-file --from $cache '.#testDerivation'
# nix -vvvvvvvvv copy --from $cache --to "file://$PWD/local?compression=zstd" '.#testDerivation'
