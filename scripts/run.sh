#!/usr/bin/env bash

# Local development runner: brings up a single-node Garage S3 server and runs
# spongix against it. Garage replaces the (abandoned, insecure) minio that this
# script used to assume. Everything lives under $cacheDir and is reused across
# runs; delete that directory to start from scratch.

set -euo pipefail

cacheDir="${SPONGIX_DEV_DIR:-/tmp/spongix}"
garageDir="$cacheDir/garage"
confFile="$garageDir/garage.toml"
credFile="$garageDir/credentials"
spongixConf="$garageDir/config.json"

region=garage
endpoint=127.0.0.1:3900
bucket=ncp

mkdir -p "$garageDir/meta" "$garageDir/data"

# Nix cache signing keys.
[ -s skey ] || nix key generate-secret --key-name spongix-dev-1 > skey
[ skey -ot pkey ] || nix key convert-secret-to-public < skey > pkey
keyFile="$PWD/skey"

# Garage server config (written once).
if [ ! -s "$confFile" ]; then
  rand_hex() { head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
  cat > "$confFile" <<EOF
metadata_dir = "$garageDir/meta"
data_dir = "$garageDir/data"
db_engine = "sqlite"
replication_factor = 1

rpc_bind_addr = "127.0.0.1:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "$(rand_hex)"

[s3_api]
s3_region = "$region"
api_bind_addr = "$endpoint"

[admin]
api_bind_addr = "127.0.0.1:3903"
admin_token = "$(rand_hex)"
EOF
fi

# Start the server and stop it when this script exits.
garage -c "$confFile" server &
garagePid=$!
trap 'kill "$garagePid" 2>/dev/null || true' EXIT

# Wait until the node answers RPC.
until garage -c "$confFile" status >/dev/null 2>&1; do sleep 0.3; done

# One-time cluster bootstrap: assign layout, create the bucket and an access key.
# The presence of the credentials file marks this as already done.
if [ ! -s "$credFile" ]; then
  nodeId=$(garage -c "$confFile" node id -q | cut -d@ -f1)
  garage -c "$confFile" layout assign -z dev -c 1GB "$nodeId"
  garage -c "$confFile" layout apply --version 1

  garage -c "$confFile" bucket create "$bucket"
  garage -c "$confFile" key create spongix >/dev/null
  garage -c "$confFile" bucket allow --read --write --owner "$bucket" --key spongix

  info=$(garage -c "$confFile" key info spongix --show-secret)
  keyId=$(awk '/Key ID:/ {print $NF}' <<<"$info")
  secret=$(awk '/Secret key:/ {print $NF}' <<<"$info")
  cat > "$credFile" <<EOF
[default]
aws_access_key_id = $keyId
aws_secret_access_key = $secret
EOF
fi

# Spongix config pointing chunks + the "public" namespace index at Garage.
cat > "$spongixConf" <<EOF
{
  "listen": "0.0.0.0:7777",
  "log_level": "debug",
  "log_mode": "development",
  "signing_key_file": "$keyFile",
  "chunks": {
    "average_size": 65536,
    "store_type": "s3",
    "s3": {
      "region": "$region",
      "url": "s3+http://$endpoint/$bucket/chunks",
      "profile": "default",
      "credentials_file": "$credFile"
    }
  },
  "namespaces": {
    "public": {
      "cache_info_priority": 50,
      "store_type": "s3",
      "s3": {
        "region": "$region",
        "url": "s3+http://$endpoint/$bucket/indices",
        "profile": "default",
        "credentials_file": "$credFile"
      },
      "substituters": ["https://cache.nixos.org"]
    }
  }
}
EOF

go run . --config "$spongixConf"
