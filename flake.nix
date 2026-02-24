{
  description = "Flake for spongix";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-25.11";
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
        lib,
        config,
        ...
      }: {
        packages = {
          symlink = pkgs.callPackage ./symlink.nix {};

          spongix = pkgs.callPackage ./package.nix {
            rev = inputs.self.rev or "dirty";
            symlinkBin = self'.packages.symlink;
          };

          spongixctl = pkgs.callPackage ./spongixctl.nix {};

          default = self'.packages.spongix;

          # just a derivation to test uploads
          testing = pkgs.runCommand "testing" {} ''
            echo testing > $out
          '';
        };

        devShells.default = pkgs.mkShell {
          nativeBuildInputs = with pkgs; [
            config.treefmt.package
            alejandra
            go
            golangci-lint
            gotools
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

        checks = lib.optionalAttrs pkgs.stdenv.isLinux {
          e2e = import ./e2e-test.nix {
            inherit pkgs;
            spongix = self'.packages.spongix;
          };
        };
      };

      flake.nixosModules = rec {
        spongix = import ./modules/spongix.nix;
        nar-proxy = import ./modules/nar-proxy.nix;
        default = spongix;
      };
    };
}
