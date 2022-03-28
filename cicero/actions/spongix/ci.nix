{
  name,
  std,
  lib,
  actionLib,
  ...
} @ args: let
  startOf = of: of.value."${name}".start;
in {
  inputs.start = ''
    "${name}": start: {
      clone_url: string
      sha: string
      statuses_url?: string
    }
  '';

  output = {start}: let
    facts = start.value."${name}".start;
  in {
    success."${name}" = {
      ok = true;
      inherit (facts) clone_url sha;
    };
  };

  job = {start}: let
    facts = start.value."${name}".start;
  in
    std.chain args [
      actionLib.simpleJob
      (std.git.clone facts)

      {
        resources = {
          memory = 1000 * 8;
          cpu = 7000;
        };
        config.console = "pipe";
        config.packages = std.data-merge.append [
          "github:input-output-hk/spongix#devShell.x86_64-linux"
        ];
      }

      (lib.optionalAttrs (facts ? statuses_url) (std.github.reportStatus facts.statuses_url))

      (std.base {})
      std.nix.develop
      (std.script "bash" ''
        set -ex
        lint
      '')
    ];
}
