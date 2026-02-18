#!/usr/bin/env nu

# Probe which requests Nix makes during fetchClosure evaluation.
# Requires the fetchclosure-probe server running on --port.
def main [
  --port: int = 9999         # port the probe server listens on
  --from-path: string        # fromPath for fetchClosure
  --to-path: string          # toPath for fetchClosure
] {
  let expr = $'builtins.fetchClosure { fromStore = "http://localhost:($port)"; fromPath = "($from_path)"; toPath = "($to_path)"; }'
  print $"Evaluating: ($expr)"
  ^nix eval --raw --expr $expr
}
