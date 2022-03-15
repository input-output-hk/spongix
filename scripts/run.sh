#!/usr/bin/env bash

set -exuo pipefail

[ -s skey ] || nix key generate-secret > skey
[ skey -ot pkey ] || nix key convert-secret-to-public < skey > pkey

# rm -rf /tmp/spongix

export MINIO_ACCESS_KEY=minioadmin
export MINIO_SECRET_KEY=minioadmin

mc mb /tmp/spongix/minio/ncp

go run . \
  --substituters \
    'https://cache.nixos.org' \
    'https://hydra.iohk.io' \
    'https://cachix.cachix.org' \
    'https://manveru.cachix.org' \
    'https://hercules-ci.cachix.org' \
  --trusted-public-keys \
    'cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=' \
    'kappa:Ffd0MaBUBrRsMCHsQ6YMmGO+tlh7EiHRFK2YfOTSwag=' \
    'hydra.iohk.io:f/Ea+s+dFdN+3Y/G+FDgSq+a5NEWhJGzdjvKNGv0/EQ=' \
    'cachix.cachix.org-1:eWNHQldwUO7G2VkjpnjDbWwy4KQ/HNxht7H4SSoMckM=' \
    'hercules-ci.cachix.org-1:ZZeDl9Va+xe9j+KqdzoBZMFJHVQ42Uu/c/1/KMC5Lw0=' \
    'manveru.cachix.org-1:L5nJHSinfA2K5dDCG3KAEadwf/e3qqhuBr7yCwSksXo=' \
  --secret-key-files ./skey \
  --listen :7777 \
  --dir /tmp/spongix \
  --log-mode development \
  --cache-size 4 \
  --bucket-url 's3+http://127.0.0.1:9000/ncp' \
  --bucket-region eu-central-1
