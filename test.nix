{
  lib,
  pkgs,
  inputs,
}:
pkgs.nixosTest {
  name = "spongix";

  testScript = ''
    cache.systemctl("is-system-running --wait")
    cache.wait_for_unit("spongix")
  '';

  nodes = {
    cache = {
      imports = [inputs.self.nixosModules.spongix];
      services.spongix = {
        package = pkgs.spongix;
        cacheDir = "/cache";
        enable = true;
      };
    };
  };
}
