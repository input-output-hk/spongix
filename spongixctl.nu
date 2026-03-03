#!/usr/bin/env nu

use std log

# --- Config ---

def get-config []: nothing -> record {
  let url = $env | get -o SPONGIX_URL | default ""
  if ($url | is-empty) {
    error make { msg: "SPONGIX_URL not set. Set it to your spongix URL including namespace (e.g. http://localhost:5000/cache)" }
  }
  let token = if ($env | get -o SPONGIX_TOKEN_FILE | default "" | is-not-empty) {
    open --raw ($env.SPONGIX_TOKEN_FILE) | str trim
  } else {
    $env | get -o SPONGIX_TOKEN | default ""
  }
  { url: $url, token: $token }
}

def auth-headers [token: string]: nothing -> list {
  if $token == "" { [] } else { [Authorization $"Bearer ($token)"] }
}

# --- Nix helpers ---

def extract-hash [input: string]: nothing -> string {
  if ($input | str starts-with "/nix/store/") {
    $input | path basename | split row "-" | first
  } else if ($input =~ '^[0-9a-df-np-sv-z]{32}$') {
    $input
  } else {
    error make { msg: $"invalid store path or hash: ($input)" }
  }
}

def build-ca [installable: string]: nothing -> record {
  log info $"Building ($installable)..."
  let from_path = ^nix build --no-link --print-out-paths $installable | str trim
  log info $"Making content-addressed: ($from_path)"
  let ca = ^nix store make-content-addressed --json $from_path | from json
  $ca.rewrites | transpose from to | get 0
}

# Check whether the NAR for a store path actually exists on the server.
# Returns true only if both the narinfo and the NAR file respond to HEAD.
def nar-exists [store: string, path: string]: nothing -> bool {
  let hash = extract-hash $path
  let narinfo_text = try { http get $"($store)/($hash).narinfo" } catch { null }
  if $narinfo_text == null { return false }
  let nar_line = $narinfo_text | lines | where { $in | str starts-with "URL: " } | get 0?
  if $nar_line == null { return false }
  let nar_path = $nar_line | str trim | str replace "URL: " ""
  try { http head $"($store)/($nar_path)" | ignore; true } catch { false }
}

def push-to-store [store: string, path: string]: nothing -> nothing {
  log info $"Pushing to ($store)..."
  ^nix copy --to $"($store)?compression=none" $path
  # nix copy treats narinfo presence as proof the data is uploaded.
  # If the NAR is still missing (stale narinfo), upload it directly.
  if not (nar-exists $store $path) {
    log warning "nix copy skipped (stale narinfo). Uploading NAR directly..."
    let hash = extract-hash $path
    let narinfo_text = try { http get $"($store)/($hash).narinfo" } catch { null }
    if $narinfo_text != null {
      let nar_line = $narinfo_text | lines | where { $in | str starts-with "URL: " } | get 0?
      if $nar_line != null {
        let nar_path = $nar_line | str trim | str replace "URL: " ""
        ^nix-store --dump $path | http put --content-type application/x-nix-nar $"($store)/($nar_path)"
      }
    }
  }
}

def current-system []: nothing -> string {
  ^nix eval --raw --impure --expr 'builtins.currentSystem'
}

def evaluate-all [system: string]: nothing -> record {
  ^nix eval --json $".#packages.($system)" --apply '
    pkgs: let
      names = builtins.filter (n: builtins.substring 0 6 n == "capkg-") (builtins.attrNames pkgs);
    in builtins.listToAttrs (builtins.map (name: let
      pkg = pkgs.${name};
      attrName = builtins.substring 6 (builtins.stringLength name - 6) name;
    in {
      inherit name;
      value = {
        storePath = pkg.outPath;
        mainProgram = pkg.meta.mainProgram or attrName;
        version = pkg.version or "0.0.1";
      };
    }) names)
  ' | from json
}

def registry-lookup [
  store: string, pkg: string, version: string, system: string
]: nothing -> any {
  let entries = try { http get $"($store)/f/($pkg)/($version)" } catch { [] }
  $entries | where system == $system | get 0?
}

def get-packages [cfg: record, packages: list<string>]: nothing -> list<string> {
  if ($packages | is-not-empty) { $packages } else {
    try { http get $"($cfg.url)/f" } catch { [] }
  }
}

# --- Commands ---

# Discover capkg-* flake outputs, build/CA/push changed, register in registry
def "main sync" [
  --json # output results as JSON
] {
  let cfg = get-config
  let system = current-system
  log info "Evaluating all packages..."
  let pkg_meta = evaluate-all $system
  let pkgs = $pkg_meta | columns
  log info $"Discovered ($pkgs | length) packages: ($pkgs | str join ', ')"

  let results = $pkgs | each {|pkg|
    let attr = $pkg | str replace "capkg-" ""
    let meta = $pkg_meta | get $pkg
    let existing = registry-lookup $cfg.url $attr $meta.version $system
    if $existing != null and $existing.fromPath == $meta.storePath and (nar-exists $cfg.url $existing.fromPath) {
      log info $"Skipping ($attr) \(outPath unchanged\)"
      { name: $attr, action: "skipped", fromPath: $meta.storePath }
    } else {
      let rewrite = build-ca $".#($pkg)"
      push-to-store $cfg.url $rewrite.from
      let body = {
        fromPath: $rewrite.from, toPath: $rewrite.to,
        mainProgram: $meta.mainProgram, system: $system,
      }
      let url = $"($cfg.url)/f/($attr)/($meta.version)"
      log info $"Registering ($attr) at ($url)..."
      http put --content-type application/json -H (auth-headers $cfg.token) $url ($body | to json)
      { name: $attr, action: "pushed", fromPath: $rewrite.from }
    }
  }

  if $json { $results | to json } else {
    let pushed = $results | where action == "pushed" | length
    let skipped = $results | where action == "skipped" | length
    log info $"=== Sync: ($pushed) pushed, ($skipped) skipped ==="
  }
}

# Build, content-address, push, and register a single package
def "main push" [
  installable: string # nix installable (e.g. .#capkg-helix or github:org/repo#pkg)
  --json # output result as JSON
] {
  let cfg = get-config
  let meta = ^nix eval --json $installable --apply '
    pkg: {
      name = pkg.pname or (builtins.parseDrvName pkg.name).name;
      version = pkg.version or "0.0.1";
      mainProgram = pkg.meta.mainProgram or (pkg.pname or (builtins.parseDrvName pkg.name).name);
      system = pkg.system;
    }
  ' | from json
  let name = $meta.name | str replace "capkg-" ""
  let rewrite = build-ca $installable
  push-to-store $cfg.url $rewrite.from
  let body = {
    fromPath: $rewrite.from, toPath: $rewrite.to,
    mainProgram: $meta.mainProgram, system: $meta.system,
  }
  let url = $"($cfg.url)/f/($name)/($meta.version)"
  log info $"Registering at ($url)..."
  http put --content-type application/json -H (auth-headers $cfg.token) $url ($body | to json)
  let result = {
    name: $name, version: $meta.version, fromPath: $rewrite.from,
    toPath: $rewrite.to, mainProgram: $meta.mainProgram, url: $url,
  }
  if $json { $result | to json } else {
    log info "Done."
    log info $"Usage: nix run 'tarball+($url).tar.gz'"
  }
}

# List all packages in the registry with pin status
def "main list" [
  --json # output as JSON
] {
  let cfg = get-config
  let pkgs = try { http get $"($cfg.url)/f" } catch { [] }
  let pin_hashes = try {
    http get -H (auth-headers $cfg.token) $"($cfg.url)/../pins"
    | each {|e| extract-hash $e.hash }
  } catch { [] }

  let entries = $pkgs | each {|pkg|
    let versions = try { http get $"($cfg.url)/f/($pkg)" } catch { [] }
    $versions | each {|ver|
      let items = try { http get $"($cfg.url)/f/($pkg)/($ver)" } catch { [] }
      $items | each {|e|
        let hash = extract-hash $e.fromPath
        $e | merge { name: $pkg, version: $ver, pinned: ($hash in $pin_hashes) }
      }
    }
  } | flatten | flatten

  if $json { $entries | to json } else { $entries | table }
}

# Check cache integrity and re-push missing store paths
def "main verify" [
  --json # output results as JSON
] {
  let cfg = get-config
  let pkgs = try { http get $"($cfg.url)/f" } catch { [] }
  let results = $pkgs | each {|pkg|
    let versions = try { http get $"($cfg.url)/f/($pkg)" } catch { [] }
    $versions | each {|ver|
      let entries = try { http get $"($cfg.url)/f/($pkg)/($ver)" } catch { [] }
      $entries | each {|e|
        # nix path-info --store only checks narinfo presence, not actual NAR data.
        # Fetch the narinfo to get the NAR URL, then HEAD the NAR directly.
        let hash = extract-hash $e.fromPath
        let narinfo_text = try { http get $"($cfg.url)/($hash).narinfo" } catch { null }
        let nar_line = if $narinfo_text != null {
          $narinfo_text | lines | where { $in | str starts-with "URL: " } | get 0?
        } else { null }
        let nar_ok = if $nar_line == null { false } else {
          let nar_path = $nar_line | str trim | str replace "URL: " ""
          try { http head $"($cfg.url)/($nar_path)" | ignore; true } catch { false }
        }
        if not $nar_ok {
          log warning $"MISSING NAR: ($pkg)/($ver) \(($e.system)\)"
          let repair_ok = if $nar_line != null {
            # narinfo exists but NAR is missing: nix copy skips paths when narinfo
            # is present (it treats narinfo as proof the data is already uploaded).
            # Directly PUT the NAR bytes instead.
            let nar_path = $nar_line | str trim | str replace "URL: " ""
            let nar_url = $"($cfg.url)/($nar_path)"
            log info $"Uploading NAR directly to ($nar_url)..."
            try { ^nix-store --dump $e.fromPath | http put --content-type application/x-nix-nar $nar_url; true } catch {|err|
              log error $"Failed to upload NAR for ($pkg)/($ver): ($err.msg)"
              false
            }
          } else {
            # narinfo also missing: full re-push via nix copy
            try { ^nix copy --to $"($cfg.url)?compression=none" $e.fromPath; true } catch {|err|
              log error $"Failed to re-push ($pkg)/($ver): ($err.msg)"
              false
            }
          }
          let status = if $repair_ok { "repushed" } else { "failed" }
          { name: $pkg, version: $ver, system: $e.system, status: $status, fromPath: $e.fromPath }
        } else {
          { name: $pkg, version: $ver, system: $e.system, status: "ok", fromPath: $e.fromPath }
        }
      }
    }
  } | flatten | flatten

  if $json { $results | to json } else {
    let ok = $results | where status == "ok" | length
    let repushed = $results | where status == "repushed" | length
    let failed = $results | where status == "failed" | length
    $results | where status != "ok" | table
    if $failed > 0 {
      log error $"=== Verify: ($ok) ok, ($repushed) repushed, ($failed) failed ==="
    } else {
      log info $"=== Verify: ($ok) ok, ($repushed) repushed ==="
    }
  }
}

# Pin a store path to prevent garbage collection
def "main pin" [
  path_or_hash: string # /nix/store/... path or 32-char hash
  label: string = ""   # optional label for the pin
] {
  let cfg = get-config
  let hash = extract-hash $path_or_hash
  http put -H (auth-headers $cfg.token) $"($cfg.url)/../pin/($hash)" $label | ignore
  print $"Pinned ($hash)"
}

# Unpin one or more store paths
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

# List all pinned store paths
def "main pins" [
  --json # output as JSON
] {
  let cfg = get-config
  let pins = http get -H (auth-headers $cfg.token) $"($cfg.url)/../pins"
  if $json { $pins | to json } else { $pins | table }
}

# Generate a nixpkgs overlay from registry packages
def "main overlay" [
  ...packages: string # package names to include (default: all)
] {
  let cfg = get-config
  let pkgs = get-packages $cfg $packages
  let lines = $pkgs | each {|p|
    ['  inherit (inputs.capkg-' $p '.packages.${system}) ' $p ';'] | str join
  }
  [
    "# Generated by: spongixctl overlay"
    "# Usage: nixpkgs.overlays = [ (import ./overlay.nix inputs) ]"
    "inputs: final: prev: let"
    "  inherit (prev.stdenv.hostPlatform) system;"
    "in {"
    ...($lines)
    "}"
  ] | str join "\n"
}

# Generate flake input declarations for registry packages
def "main inputs" [
  ...packages: string # package names to include (default: all)
] {
  let cfg = get-config
  let pkgs = get-packages $cfg $packages
  $pkgs | each {|p|
    $"capkg-($p).url = \"tarball+($cfg.url)/f/($p)/*.tar.gz\";"
  } | str join "\n"
}

def main [] {
  help main
}
