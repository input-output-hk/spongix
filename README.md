<div align="center">
  <img src="img/spongix.svg" width="250" />
  <h1>Spongix</h1>
  <p>A proxy that acts as binary cache for Nix</span>
</div>

* Signs Narinfo in flight with own private key
* Authenticates with S3 to forward NARs for long-term storage
* Keeps a local cache on disk for faster responses.
* Provides a minimal Docker registry

## Usage

Start `spongix`:

    nix key generate-secret --key-name foo > skey
    nix build
    ./result/bin/spongix \
      --substituters "https://cache.nixos.org" "https://hydra.iohk.io" \
      --secret-key-files ./skey \
      --trusted-public-keys "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=" "hydra.iohk.io:f/Ea+s+dFdN+3Y/G+FDgSq+a5NEWhJGzdjvKNGv0/EQ=" \
      --listen :7745 \
      --dir /tmp/spongix

To add store paths to the cache, you can use `nix copy`:

    nix copy --to 'http://127.0.0.1:7745?compression=none' github:nixos/nix

To use this as your binary cache, specify it as a substituter:

    nix build github:nixos/nix \
      --option substituters http://127.0.0.1:7745 \
      --option trusted-public-keys "$(< pkey)"

Signatures are checked against the the `trusted-public-keys` of your
configuration.

### Upload after every build

Set a `post-build-hook` in your nix configuration to a script like this:

    #!/bin/sh
    set -euf
    export IFS=' '
    if [[ -n "$OUT_PATHS" ]]; then
      echo "Uploading to cache: $OUT_PATHS"
      exec nix copy --to 'http://127.0.0.1:7745?compression=none' $OUT_PATHS
    fi

## TODO

- [ ] Healthchecks
- [ ] A way to horizontally scale
- [ ] Proper CLI usage
- [ ] Benchmark of desync index vs db lookup performance
- [ ] Additional signing for a set of allowed public keys
- [ ] Disk cache size limits and LRU eviction
- [ ] Forward lookups across multiple upstream caches
- [ ] Identify and solve concurrency issues
- [ ] Prometheus metrics
- [ ] Store narinfo in a database
- [ ] Upload to S3 compatible storage as well as the local store
- [ ] Verify existing signatures

## Pebble Storage Tuning

When using Pebble as the chunk store backend (`store_type: "pebble"`), performance
on HDDs is highly sensitive to three configuration knobs. The benchmarks below were
run on a ZFS raidz1 array with 100MB NARs and 64KB average chunk size.

### Configuration options

| JSON key | NixOS option | Default | Description |
|---|---|---|---|
| `pebble_memtable_size` | `chunks.pebbleMemTableSize` | 67108864 (64MB) | Pebble memtable size in bytes |
| `pebble_l0_compaction_threshold` | `chunks.pebbleL0CompactionThreshold` | 4 | L0 file count before compaction triggers |
| `pebble_wal_dir` | `chunks.pebbleWALDir` | same as data dir | Separate directory for the write-ahead log |
| `batch_max_bytes` | `chunks.batchMaxBytes` | 268435456 (256MB) | Max bytes to buffer before flushing to Pebble |

### Recommendations

**Move the WAL to fast storage.** This is the single biggest win. Placing the WAL
on tmpfs or an SSD nearly doubles throughput compared to WAL-on-HDD at every
configuration. On a production server with spinning disks, set `pebble_wal_dir` to
a path on tmpfs (e.g. `/run/spongix/wal`) or an SSD partition.

Note: WAL on tmpfs means uncommitted writes are lost on power failure. This is
acceptable for a cache proxy since data can be re-fetched from upstream.

**Increase the memtable size to 256MB.** The default 64MB memtable forces frequent
flushes during large NAR ingestion (~1,536 chunks per 100MB NAR). Increasing to
256MB keeps most inserts in memory until the final atomic batch commit.

**L0 compaction threshold has minimal impact.** Values of 4, 8, or 12 produce
nearly identical throughput (<5% variance). The default of 4 is fine; 8 is a
reasonable middle ground if you want fewer compactions.

### Benchmark results (100MB NARs, HDD)

| MemTable | L0 | WAL | Throughput | Latency |
|---|---|---|---|---|
| 256MB | 8 | tmpfs | 194 MB/s | 541ms |
| 256MB | 4 | tmpfs | 194 MB/s | 541ms |
| 256MB | 12 | tmpfs | 196 MB/s | 536ms |
| 128MB | 4 | tmpfs | 189 MB/s | 554ms |
| 128MB | 8 | tmpfs | 188 MB/s | 559ms |
| 256MB | 8 | HDD | 158 MB/s | 664ms |
| 256MB | 4 | HDD | 155 MB/s | 677ms |
| 128MB | 8 | HDD | 148 MB/s | 710ms |
| 64MB | 4 | tmpfs | 71 MB/s | 1.48s |
| 64MB | 4 | HDD | 42 MB/s | 2.50s |

For comparison, the baseline (64MB memtable, WAL on HDD, no batching) achieved
19.6 MB/s. The recommended config (256MB memtable, WAL on tmpfs) achieves
194 MB/s -- a **10x improvement**.

### Example config (JSON)

```json
{
  "chunks": {
    "store_type": "pebble",
    "pebble_dir": "/big/spongix/pebble",
    "pebble_wal_dir": "/run/spongix/wal",
    "pebble_memtable_size": 268435456,
    "pebble_l0_compaction_threshold": 8,
    "batch_max_bytes": 268435456
  }
}
```

### Example NixOS config

```nix
services.spongix.chunks = {
  storeType = "pebble";
  pebbleDir = "/big/spongix/pebble";
  pebbleWALDir = "/run/spongix/wal";
  pebbleMemTableSize = 256 * 1024 * 1024;  # 256MB
  pebbleL0CompactionThreshold = 8;
  batchMaxBytes = 256 * 1024 * 1024;  # 256MB
};
```

## Storage

R2:
* `/bucket/$namespace/$hash.narinfo`
* `/bucket/$namespace/nar/$hash.nar`
* `/bucket/$namespace/log/$hash.drv`
* `/bucket/$namespace/realisations/sha256:$hash.doi`

Pipeline:

* Upload NAR

## Issues

Implementing a Nix cache that can handle deduplication and compressed NAR files
is a very tricky task.
The `FileHash` and `FileSize` properties of `.narinfo` files are mainly useful
for static cashes that store files without any modification.
We want, however, to store NARs uncompressed for optimal deduplication while
transmitting them compressed for optimal speed.

While that sounds easy on the outset, the problem comes down to reproducability.
For example `xz` compression gives no guarantees that running the same
compression twice will result in the same output.
For `zstd` there is (for now), a guarantee that the same version of `zstd` will
always return the same output, and it can uncompress the output of earlier
versions, but newer versions most likely will produce different output.

So, simply trusting the transmitted `.narinfo` with its `FileHash`/`FileSize`
is not an option, since we don't know what version and parameters were used to
create the compressed file and won't be able to replicate it anyway in a lot of
cases.

Additionally, the `.narinfo` is somewhat write-once, due to potential caching
across mutliple layers. If we were to return a different info on every request,
that would not be very cacheable, and if Spongix were to upgrade and suddenly
return different hashes, that would cause issues with clients that still have
older narinfos in their cache.
