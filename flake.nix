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
        go = prev.go_1_18;
        golangci-lint = prev.golangci-lint.override {buildGoModule = prev.buildGo118Module;};
        gotools = prev.gotools.override {buildGoModule = prev.buildGo118Module;};
        gocode = prev.gocode.override {buildGoPackage = prev.buildGo118Package;};

        alejandra = inputs.alejandra.defaultPackage.x86_64-linux;
        spongix = prev.callPackage ./package.nix {
          inherit (inputs.inclusive.lib) inclusive;
          rev = self.rev or "dirty";
        };
      };

      packages = {
        spongix,
        hello,
        cowsay,
        ponysay,
        lib,
        coreutils,
        bashInteractive,
      }: {
        inherit spongix;
        defaultPackage = spongix;

        oci-tiny = inputs.n2c.packages.x86_64-linux.nix2container.buildImage {
          name = "localhost:7777/spongix";
          tag = "v1";
          config = {
            Cmd = ["${ponysay}/bin/ponysay" "hi"];
            Env = [
              "PATH=${lib.makeBinPath [coreutils bashInteractive]}"
            ];
          };
          maxLayers = 128;
        };

        oci = inputs.n2c.packages.x86_64-linux.nix2container.buildImage {
          name = "localhost:7745/spongix";
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
