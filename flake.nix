{
  description = "Flake for spongix";

  inputs = {
    devshell.url = "github:numtide/devshell";
    inclusive.url = "github:input-output-hk/nix-inclusive";
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";
    utils.url = "github:kreisys/flake-utils";
    cicero.url = "github:input-output-hk/cicero";
    n2c.url = "github:nlewo/nix2container";
    alejandra.url = "github:kamadorueda/alejandra";
  };

  outputs = {
    self,
    nixpkgs,
    utils,
    devshell,
    cicero,
    ...
  } @ inputs:
    utils.lib.simpleFlake {
      systems = ["x86_64-linux"];
      inherit nixpkgs;

      preOverlays = [devshell.overlay];

      overlay = final: prev: {
        alejandra = inputs.alejandra.defaultPackage.x86_64-linux;
        spongix = prev.callPackage ./package.nix {
          inherit (inputs.inclusive.lib) inclusive;
          rev = self.rev or "dirty";
        };
      };

      packages = {
        spongix,
        hello,
      }: {
        inherit spongix;
        defaultPackage = spongix;

        oci = inputs.n2c.packages.x86_64-linux.nix2container.buildImage {
          name = "docker.infra.aws.iohkdev.io/spongix";
          tag = spongix.version;
          config = {
            entrypoint = ["${spongix}/bin/spongix"];
            environment = ["CACHE_DIR=/cache"];
          };
          maxLayers = 250;
        };
      };

      hydraJobs = {
        spongix,
        callPackage,
      }: {
        inherit spongix;
        test = callPackage ./test.nix {inherit inputs;};
      };

      nixosModules.spongix = import ./module.nix;

      devShell = {devshell}: devshell.fromTOML ./devshell.toml;

      extraOutputs.ciceroActions =
        cicero.lib.callActionsWithExtraArgs rec {
          inherit (cicero.lib) std;
          inherit (nixpkgs) lib;
          actionLib = import "${cicero}/action-lib.nix" {inherit std lib;};
        }
        ./cicero/actions;
    };
}
