{
  description = "Flake for spongix";

  inputs = {
    inclusive.url = "github:input-output-hk/nix-inclusive";
    nixpkgs.url = "github:nixos/nixpkgs/nixos-23.05";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs:
    inputs.flake-parts.lib.mkFlake {inherit inputs;} {
      systems = ["x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin"];

      imports = [
        inputs.flake-parts.flakeModules.easyOverlay
        inputs.treefmt-nix.flakeModule
      ];

      perSystem = {
        self',
        pkgs,
        config,
        ...
      }: {
        packages = {
          spongix = pkgs.callPackage ./package.nix {
            inherit (inputs.inclusive.lib) inclusive;
            rev = inputs.self.rev or "dirty";
          };

          default = self'.packages.spongix;

          # just a derivation to test uploads
          testing = pkgs.runCommand "testing" {} ''
            echo testing > $out
          '';
        };

        devShells.default = pkgs.mkShell {
          nativeBuildInputs = with pkgs; [
            config.treefmt.package
            go
            golangci-lint
            gotools
            gocode
            gopls
            nodejs
            minio
            minio-client
            watchexec
          ];
        };

        treefmt = {
          programs.alejandra.enable = true;
          programs.gofmt.enable = true;
          projectRootFile = "flake.nix";
        };
      };

      flake.nixosModules = rec {
        spongix = import ./modules/spongix.nix;
        nar-proxy = import ./modules/nar-proxy.nix;
        default = spongix;
      };
    };
}
