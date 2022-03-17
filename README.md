<div align="center">
  <img src="https://github.cominput-output-hk/spongix/raw/main/img/spongix.svg" width="250" />
  <h1>Spongix</h1>
  <p>A proxy that acts as binary cache for Nix</span>
</div>

* Signs Narinfo in flight with own private key
* Authenticates with S3 to forward NARs for long-term storage
* Keeps a local cache on disk for faster responses.

## Usage

Start `spongix`:

    nix key generate-secret --key-name foo > skey
    nix build
    ./result/bin/spongix \
      --substituters "https://cache.nixos.org" "https://hydra.iohk.io" \
      --secret-key-files ./skey \
      --trusted-public-keys "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=" "hydra.iohk.io:f/Ea+s+dFdN+3Y/G+FDgSq+a5NEWhJGzdjvKNGv0/EQ=" \
      --listen :7777 \
      --dir /tmp/spongix

To add store paths to the cache, you can use `nix copy`:

    nix copy --to 'http://127.0.0.1:7777?compression=none' github:nixos/nix

To use this as your binary cache, specify it as a substituter:

    nix build github:nixos/nix \
      --option substituters http://127.0.0.1:7777 \
      --option trusted-public-keys "$(< pkey)"

Signatures are checked against the the `trusted-public-keys` of your
configuration.

### Upload after every build

Set a `post-build-hook` in your nix configuration to a script like this:

    #!/bin/sh
    set -euf
    export IFS=' '
    echo "Uploading to cache: $OUT_PATHS"
    exec nix copy --to 'http://127.0.0.1:7777?compression=none' $OUT_PATHS

## TODO

- [ ] Write better integration tests (with cicero)
- [x] Verify existing signatures
- [x] Store narinfo in a database
- [x] Upload to S3 as well as the local store
- [x] Identify and solve concurrency issues
- [ ] A way to horizontally scale (probably by just locking via consul, s3, raft, postgres, rqlite, dqlite, ...)
- [x] Additional signing for a set of allowed public keys
- [x] Disk cache size limits and LRU eviction
- [ ] Benchmark of desync index vs db lookup performance
- [x] Forward lookups across multiple upstream caches
- [ ] Proper CLI usage
- [x] Prometheus metrics
- [ ] Healthchecks
