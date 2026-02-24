{
  stdenv,
  nushell,
  lib,
}:
stdenv.mkDerivation {
  name = "spongixctl";
  src = ./spongixctl.nu;
  dontUnpack = true;
  installPhase = ''
    mkdir -p $out/bin
    substitute $src $out/bin/spongixctl \
      --replace-fail '#!/usr/bin/env nu' '#!${lib.getExe nushell}'
    chmod +x $out/bin/spongixctl
  '';
}
