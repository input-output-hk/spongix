#!/usr/bin/env bash

pkgs="$(
  nix eval --impure --json --expr '__attrNames (builtins.getFlake "nixpkgs").legacyPackages.x86_64-linux' | jq -r '.[]'
)"

for pkg in $pkgs; do
  echo "copying $pkg"
  nix copy --to http://localhost:7777/local?compression=none --option post-build-hook '' "nixpkgs#${pkg}"
  echo "db size: $(du test.sqlite) $(du -h test.sqlite)"
  sleep 0.1
done
