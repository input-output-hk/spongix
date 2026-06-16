#!/usr/bin/env nu

# spongixctl - manage spongix's cache pin API.
#
# The package registry moved to its own project (capkg); its publishing client
# is capkg.nu. spongixctl now only drives spongix's global Pin API, which keeps
# store paths from being garbage-collected.
#
#   SPONGIX_URL         spongix URL (a trailing namespace segment is fine; pins
#                       are DB-wide and the namespace is stripped)
#   SPONGIX_TOKEN       Pin API bearer token
#   SPONGIX_TOKEN_FILE  read the token from a file instead

use std log

# --- Config ---

def get-config []: nothing -> record {
  let url = $env | get -o SPONGIX_URL | default ""
  if ($url | is-empty) {
    error make { msg: "SPONGIX_URL not set. Set it to your spongix URL (e.g. http://localhost:7745)" }
  }
  let token = if ($env | get -o SPONGIX_TOKEN_FILE | default "" | is-not-empty) {
    open --raw ($env.SPONGIX_TOKEN_FILE) | str trim
  } else {
    $env | get -o SPONGIX_TOKEN | default ""
  }
  { url: $url, token: $token }
}

def auth-headers [token: string]: nothing -> list {
  if ($token | is-empty) { [] } else { [Authorization $"Bearer ($token)"] }
}

# Pins are global (not namespaced); `/../` drops any trailing namespace segment.
def extract-hash [input: string]: nothing -> string {
  if ($input | str starts-with "/nix/store/") {
    $input | path basename | split row "-" | first
  } else if ($input =~ '^[0-9a-df-np-sv-z]{32}$') {
    $input
  } else {
    error make { msg: $"invalid store path or hash: ($input)" }
  }
}

# --- Commands ---

# Pin a store path to prevent garbage collection.
def "main pin" [
  path_or_hash: string # /nix/store/... path or 32-char hash
  label: string = ""   # optional label for the pin
] {
  let cfg = get-config
  let hash = extract-hash $path_or_hash
  http put -H (auth-headers $cfg.token) $"($cfg.url)/../pin/($hash)" $label | ignore
  print $"Pinned ($hash)"
}

# Unpin one or more store paths.
def "main unpin" [
  ...paths_or_hashes: string # store paths or 32-char hashes to unpin
] {
  let cfg = get-config
  for arg in $paths_or_hashes {
    let hash = extract-hash $arg
    http delete -H (auth-headers $cfg.token) $"($cfg.url)/../pin/($hash)" | ignore
    print $"Unpinned ($hash)"
  }
}

# List all pinned store paths.
def "main pins" [
  --json # output as JSON
] {
  let cfg = get-config
  let pins = http get -H (auth-headers $cfg.token) $"($cfg.url)/../pins"
  if $json { $pins | to json } else { $pins | table }
}

def main [] {
  help main
}
