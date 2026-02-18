#!/usr/bin/env nu

use std log

const capkg_prefix = "capkg-"
const json_file = "capkgs.json"

# Resolve a flag value: use flag if non-empty, else env var, else empty string
def resolve-env [flag: string, env_name: string]: nothing -> string {
  if $flag != "" { $flag } else { $env | get -o $env_name | default "" }
}

# Build auth headers: empty list if no token, otherwise [Authorization "Bearer ..."]
def auth-headers [token: string]: nothing -> list {
  if $token == "" { [] } else { [Authorization $"Bearer ($token)"] }
}

# Build, make content-addressed, and push a single installable
def process-one [
  installable: string
  --store: string
]: nothing -> record {
  log info $"Building ($installable)..."
  let from_path = ^nix build --no-link --print-out-paths $installable | str trim

  log info $"Making content-addressed: ($from_path)"
  let ca = ^nix store make-content-addressed --json $from_path | from json
  let rewrite = $ca.rewrites | transpose from to | get 0

  log info $"  from: ($rewrite.from)"
  log info $"  to:   ($rewrite.to)"

  log info $"Pushing to ($store)..."
  ^nix copy --to $"($store)?compression=none" $rewrite.from

  $rewrite
}

# Discover capkg-* flake outputs and extract metadata in a single nix eval
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
        drvPath = pkg.drvPath;
        mainProgram = pkg.meta.mainProgram or attrName;
        version = pkg.version or "0.0.1";
      };
    }) names)
  ' | from json
}

# Build, CA, push, and register a single package
def "main push" [
  installable: string              # Nix installable to build (e.g. .#package)
  --store: string = ""             # Spongix store URL (or set SPONGIX_STORE env)
  --owner: string                  # package owner
  --name: string                   # package name
  --version: string                # package version
  --main-program (-m): string = "" # override mainProgram auto-detection
  --token: string = ""             # bearer token (or set SPONGIX_TOKEN env)
] {
  let store = resolve-env $store SPONGIX_STORE
  let token = resolve-env $token SPONGIX_TOKEN

  let main_prog = if $main_program != "" {
    $main_program
  } else {
    try {
      ^nix eval --raw $"($installable).meta.mainProgram"
    } catch {
      $installable | split row "#" | last | split row "." | last
    }
  }

  let rewrite = process-one $installable --store $store

  let system = ^nix eval --raw --impure --expr 'builtins.currentSystem'
  let body = {
    fromPath: $rewrite.from
    toPath: $rewrite.to
    mainProgram: $main_prog
    system: $system
  }

  let url = $"($store)/f/($owner)/($name)/($version)"
  log info $"Registering at ($url)..."
  http put --content-type application/json -H (auth-headers $token) $url ($body | to json)
  log info "Done."
  log info $"Usage: nix run 'tarball+($url).tar.gz'"
}

# Discover capkg-* packages, build/push changed, register all
def "main sync" [
  --store: string = ""    # Spongix store URL (or set SPONGIX_STORE env)
  --owner: string         # package owner for registry
  --token: string = ""    # bearer token (or set SPONGIX_TOKEN env)
] {
  let store = resolve-env $store SPONGIX_STORE
  let token = resolve-env $token SPONGIX_TOKEN
  let system = ^nix eval --raw --impure --expr 'builtins.currentSystem'

  log info "Evaluating all packages..."
  let pkg_meta = evaluate-all $system
  let pkgs = $pkg_meta | columns
  log info $"Discovered ($pkgs | length) packages: ($pkgs | str join ', ')"

  let old_entries = if ($json_file | path exists) { open $json_file } else { {} }

  let results = $pkgs | each {|pkg|
    let attr = $pkg | str replace $capkg_prefix ""
    let meta = $pkg_meta | get $pkg
    let hash = $meta.drvPath | hash sha256

    let cached = $old_entries | transpose k v
      | where {|row| $row.k == $attr and ($row.v | get -o inputHash) == $hash }

    if ($cached | is-not-empty) {
      log info $"Skipping ($pkg) \(cached\)"
      { attr_name: $attr, entry: ($cached | first | get v) }
    } else {
      let rewrite = process-one $".#($pkg)" --store $store

      let body = {
        fromPath: $rewrite.from
        toPath: $rewrite.to
        mainProgram: $meta.mainProgram
        system: $system
      }
      let url = $"($store)/f/($owner)/($attr)/($meta.version)"
      log info $"Registering ($attr) at ($url)..."
      http put --content-type application/json -H (auth-headers $token) $url ($body | to json)

      {
        attr_name: $attr
        entry: {
          fromStore: $store
          fromPath: $rewrite.from
          toPath: $rewrite.to
          mainProgram: $meta.mainProgram
          version: $meta.version
          inputHash: $hash
        }
      }
    }
  }

  let new_entries = $results | reduce --fold {} {|it, acc| $acc | insert $it.attr_name $it.entry }

  # Diff reporting
  let old_keys = $old_entries | columns
  let new_keys = $new_entries | columns
  let added = $new_keys | where { $in not-in $old_keys }
  let removed = $old_keys | where { $in not-in $new_keys }
  let common = $new_keys | where { $in in $old_keys }
  let changed = $common | where {|k|
    ($old_entries | get $k | reject -o inputHash) != ($new_entries | get $k | reject -o inputHash)
  }
  let unchanged = $common | where {|k|
    ($old_entries | get $k | reject -o inputHash) == ($new_entries | get $k | reject -o inputHash)
  }

  let counts = [
    $"($added | length) added"
    $"($changed | length) changed"
    $"($removed | length) removed"
    $"($unchanged | length) unchanged"
  ] | str join ', '
  log info $"=== Sync Summary: ($counts) ==="
  for a in $added { log info $"+ ($a) ($new_entries | get $a | get -o version | default '')" }
  for c in $changed {
    let old_ver = $old_entries | get $c | get -o version | default '?'
    let new_ver = $new_entries | get $c | get -o version | default '?'
    log info $"~ ($c) ($old_ver) -> ($new_ver)"
  }
  for r in $removed { log warning $"- ($r)" }

  $new_entries | to json --indent 2 | save --force $json_file
  log info $"Wrote ($json_file)"
}

# Check cache integrity and re-push missing entries
def "main verify" [
  --store: string = ""  # override fromStore in entries (or set SPONGIX_STORE env)
] {
  if not ($json_file | path exists) {
    error make { msg: $"($json_file) not found" }
  }

  let entries = open $json_file
  let names = $entries | columns
  log info $"Verifying ($names | length) entries in cache..."

  mut missing = []
  for name in $names {
    let entry = $entries | get $name
    let check_store = if $store != "" { $store } else { $entry.fromStore }
    let present = try {
      ^nix path-info --store $check_store $entry.fromPath o+e>| ignore
      true
    } catch {
      false
    }

    if $present {
      log info $"  OK: ($name) \(($entry.fromPath)\)"
    } else {
      log warning $"  MISSING: ($name) \(($entry.fromPath)\)"
      $missing = ($missing | append $name)
    }
  }

  if ($missing | is-empty) {
    log info "All entries present in cache."
    return
  }

  log warning $"($missing | length) missing entries, re-pushing..."
  for name in $missing {
    let entry = $entries | get $name
    let check_store = if $store != "" { $store } else { $entry.fromStore }
    log info $"  Re-pushing ($name)..."
    ^nix copy --to $"($check_store)?compression=none" $entry.fromPath
    log info $"  Pushed ($name)"
  }

  log info "=== Verify Summary ==="
  log info $"  Present: (($names | length) - ($missing | length))/($names | length)"
  log info $"  Re-pushed: ($missing | str join ', ')"
}

# Push content-addressed Nix packages to a Spongix registry.
#
# Subcommands:
#   push    Build, CA, push, and register a single package
#   sync    Discover capkg-* packages, build/push changed, register all
#   verify  Check cache integrity and re-push missing entries
#
# Environment:
#   SPONGIX_STORE  Default store URL (e.g. http://alpha:5000/cache)
#   SPONGIX_TOKEN  Bearer token for registry auth
def main [] {
  help main
}
