{ name, std, lib, actionLib, ... }@args:
let startOf = of: of.value."${name}".start;
in {
  inputs.start = ''
    "${name}": start: clone_url: string
  '';

  output = { start }: {
    success."${name}" = {
      ok = true;
      inherit (startOf start) clone_url;
    };
  };

  job = { start }:
    std.chain args [
      actionLib.simpleJob
      (std.script "bash" ''
        echo hi
      '')
    ];
}
