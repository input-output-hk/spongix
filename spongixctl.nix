{
  writeScriptBin,
  nushell,
  lib,
}:
writeScriptBin "spongixctl" ''
  #!${lib.getExe nushell}

  const usage = "Usage: spongixctl <pin|unpin|list> [args...]"

  def get-config []: nothing -> record {
    let url = $env | get -o SPONGIX_URL | default "http://localhost:5000"
    let token_file = $env | get -o SPONGIX_TOKEN_FILE | default ""
    if $token_file == "" {
      error make { msg: "SPONGIX_TOKEN_FILE not set" }
    }
    let token = open --raw $token_file | str trim
    { url: $url, token: $token }
  }

  def extract-hash [input: string]: nothing -> string {
    if ($input | str starts-with "/nix/store/") {
      $input | path basename | split row "-" | first
    } else if ($input =~ '^[0-9a-df-np-sv-z]{32}$') {
      $input
    } else {
      error make { msg: $"invalid store path or hash: ($input)" }
    }
  }

  def auth-headers [token: string]: nothing -> record {
    { Authorization: $"Bearer ($token)" }
  }

  def "main pin" [
    path_or_hash: string  # store path or 32-char hash
    label: string = ""    # optional label for the pin
  ] {
    let cfg = get-config
    let hash = extract-hash $path_or_hash
    http put -H (auth-headers $cfg.token) $"($cfg.url)/pin/($hash)" $label | ignore
    print $"Pinned ($hash)"
  }

  def "main unpin" [...paths_or_hashes: string] {
    let cfg = get-config
    for arg in $paths_or_hashes {
      let hash = extract-hash $arg
      http delete -H (auth-headers $cfg.token) $"($cfg.url)/pin/($hash)" | ignore
      print $"Unpinned ($hash)"
    }
  }

  def "main list" [] {
    let cfg = get-config
    http get -H (auth-headers $cfg.token) $"($cfg.url)/pins"
    | each {|e| $"($e.hash)\t($e.label)" }
    | str join "\n"
    | print
  }

  def main [] {
    print $usage
  }
''
