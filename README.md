# Nix Store Proxy

A proxy that acts as binary cache for Nix.

* Signs Narinfo in flight with own private key
* Authenticates with S3 to forward NARs for long-term storage
* Keeps a local cache on disk for faster responses.

## Usage

To add store paths to the cache, you can use `nix copy`:

    nix copy --to 's3://cache?endpoint=127.0.0.1:7070&scheme=http' .#foo

To use this as your binary cache, specify it as a substituter:

    nix build --option substituters http://127.0.0.1:7070 .#foo

Public and private keys are inherited from the Nix configuration. So if your
system already has a private key specified, you won't need to that again.

Signatures are checked against the the `trusted-public-keys` of your
configuration.

### Upload after every build

Set a `post-build-hook` in your nix configuration to a script like this:

    #!/bin/sh
    set -euf
    export IFS=' '
    echo "Uploading to cache: $OUT_PATHS"
    exec nix copy --to 's3://cache?endpoint=127.0.0.1:7070&scheme=http' $OUT_PATHS

## TODO

- [x] Write tests
- [x] Distribute lookups across multiple caches
- [x] Proper CLI usage
- [x] Verify existing signatures
- [ ] Disk cache size limits and LRU eviction
- [ ] Metrics for cache usage
