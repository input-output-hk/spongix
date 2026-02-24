#!/usr/bin/env nu

use std log

# Resolve a flag value: use flag if non-empty, else env var, else empty string
def resolve-env [flag: string, env_name: string]: nothing -> string {
  if $flag != "" { $flag } else { $env | get -o $env_name | default "" }
}

# Build auth headers: empty list if no token, otherwise [Authorization "Bearer ..."]
def auth-headers [token: string]: nothing -> list {
  if $token == "" { [] } else { [Authorization $"Bearer ($token)"] }
}

# Build, make content-addressed, and return the rewrite paths (does not push)
def build-ca [installable: string]: nothing -> record {
  log info $"Building ($installable)..."
  let from_path = ^nix build --no-link --print-out-paths $installable | str trim

  log info $"Making content-addressed: ($from_path)"
  let ca = ^nix store make-content-addressed --json $from_path | from json
  let rewrite = $ca.rewrites | transpose from to | get 0

  log info $"  from: ($rewrite.from)"
  log info $"  to:   ($rewrite.to)"

  $rewrite
}

# Copy a store path to the remote cache
def push-to-store [store: string, path: string]: nothing -> nothing {
  log info $"Pushing to ($store)..."
  ^nix copy --to $"($store)?compression=none" $path
}

# Look up an existing registry entry for a specific system
def registry-lookup [
  store: string, pkg: string, version: string, system: string
]: nothing -> any {
  let entries = try { http get $"($store)/f/($pkg)/($version)" } catch { [] }
  $entries | where system == $system | get -o 0
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

  let rewrite = build-ca $installable
  push-to-store $store $rewrite.from

  let system = ^nix eval --raw --impure --expr 'builtins.currentSystem'
  let body = {
    fromPath: $rewrite.from
    toPath: $rewrite.to
    mainProgram: $main_prog
    system: $system
  }

  let url = $"($store)/f/($name)/($version)"
  log info $"Registering at ($url)..."
  http put --content-type application/json -H (auth-headers $token) $url ($body | to json)
  log info "Done."
  log info $"Usage: nix run 'tarball+($url).tar.gz'"
}

# Discover capkg-* packages, build/push changed, register all
#
# For each package: builds + CA (instant when cached), compares the
# resulting fromPath against the registry entry. Only pushes and
# re-registers when the fromPath has changed.
def "main sync" [
  --store: string = ""    # Spongix store URL (or set SPONGIX_STORE env)
  --token: string = ""    # bearer token (or set SPONGIX_TOKEN env)
] {
  let store = resolve-env $store SPONGIX_STORE
  let token = resolve-env $token SPONGIX_TOKEN
  let system = ^nix eval --raw --impure --expr 'builtins.currentSystem'

  log info "Evaluating all packages..."
  let pkg_meta = evaluate-all $system
  let pkgs = $pkg_meta | columns
  log info $"Discovered ($pkgs | length) packages: ($pkgs | str join ', ')"

  mut pushed = 0
  mut skipped = 0

  for pkg in $pkgs {
    let attr = $pkg | str replace "capkg-" ""
    let meta = $pkg_meta | get $pkg

    let rewrite = build-ca $".#($pkg)"

    let existing = registry-lookup $store $attr $meta.version $system
    if $existing != null and $existing.fromPath == $rewrite.from {
      log info $"Skipping ($attr) \(registry match\)"
      $skipped += 1
    } else {
      push-to-store $store $rewrite.from

      let body = {
        fromPath: $rewrite.from
        toPath: $rewrite.to
        mainProgram: $meta.mainProgram
        system: $system
      }
      let url = $"($store)/f/($attr)/($meta.version)"
      log info $"Registering ($attr) at ($url)..."
      http put --content-type application/json -H (auth-headers $token) $url ($body | to json)
      $pushed += 1
    }
  }

  log info $"=== Sync Summary: ($pushed) pushed, ($skipped) skipped ==="
}

# Check cache integrity by walking the registry and re-pushing missing entries
def "main verify" [
  --store: string = ""   # Spongix store URL (or set SPONGIX_STORE env)
  --token: string = ""   # bearer token (or set SPONGIX_TOKEN env)
] {
  let store = resolve-env $store SPONGIX_STORE
  let token = resolve-env $token SPONGIX_TOKEN

  let packages = try { http get $"($store)/f" } catch { [] }
  if ($packages | is-empty) {
    log info "No packages found in registry."
    return
  }

  mut total = 0
  mut missing_count = 0

  for pkg in $packages {
    let versions = try { http get $"($store)/f/($pkg)" } catch { [] }

    for version in $versions {
      let entries = try { http get $"($store)/f/($pkg)/($version)" } catch { [] }

      for entry in $entries {
        $total += 1
        let present = try {
          ^nix path-info --store $store $entry.fromPath o+e>| ignore
          true
        } catch {
          false
        }

        if $present {
          log info $"  OK: ($pkg)/($version) \(($entry.system)\)"
        } else {
          log warning $"  MISSING: ($pkg)/($version) \(($entry.system)\) ($entry.fromPath)"
          $missing_count += 1

          log info $"  Re-pushing ($pkg)/($version)..."
          ^nix copy --to $"($store)?compression=none" $entry.fromPath
          log info $"  Pushed ($pkg)/($version)"
        }
      }
    }
  }

  log info "=== Verify Summary ==="
  log info $"  Total: ($total), Present: ($total - $missing_count), Re-pushed: ($missing_count)"
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
