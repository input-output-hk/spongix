{ name, std, lib, actionLib, ... }@args:
let startOf = of: of.value."${name}".start;
in {
  inputs.start = ''
    "${name}": start: {
      clone_url: string
      sha: string
      statuses_url?: string
    }
  '';

  output = { start }: {
    success."${name}" = {
      ok = true;
      inherit (start.value."${name}".start) clone_url;
    };
  };

  job = { start }:
    let facts = start.value."cicero/ci".start;
    in std.chain args [
      actionLib.simpleJob

      {
        resources.memory = 1024 * 3;
        config.packages = std.data-merge.append [
          "github:input-output-hk/nix-cache-proxy/${facts.sha}#devShell.x86_64-linux"
        ];
      }

      (lib.optionalAttrs (facts ? statuses_url)
        (std.github.reportStatus facts.statuses_url))

      (std.git.clone facts)

      (std.script "bash" ''
        set -ex
        lint
        ${lib.escapeShellArgs next}
      '')

      std.nix.build
    ];
}
