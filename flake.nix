{
  description = "Flake for nix-cache-proxy";

  inputs = {
    devshell.url = "github:numtide/devshell";
    inclusive.url = "github:input-output-hk/nix-inclusive";
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";
    utils.url = "github:kreisys/flake-utils";
  };

  outputs = { self, nixpkgs, utils, devshell, ... }@inputs:
    utils.lib.simpleFlake {
      systems = [ "x86_64-linux" ];
      inherit nixpkgs;

      preOverlays = [ devshell.overlay ];

      overlay = final: prev: {
        foo = prev.runCommand "foo" { } ''
          echo ${prev.hello} > $out
        '';

        go = prev.go_1_17;

        nix-cache-proxy = prev.buildGoModule rec {
          pname = "nix-cache-proxy";
          version = "2021.11.25.001";
          vendorSha256 = "sha256-MntNIftZu9+WIfc5xgwDIehAPLGUjLMFGWx2DhQ+pNk=";

          src = inputs.inclusive.lib.inclusive ./. [
            ./fixtures
            ./go.mod
            ./go.sum
            ./helpers.go
            ./main.go
            ./narinfo.go
            ./narinfo_test.go
            ./nix_config.go
            ./router.go
            ./routing_test.go
          ];

          CGO_ENABLED = "0";
          GOOS = "linux";

          ldflags = [
            "-s"
            "-w"
            "-extldflags"
            "-static"
            "-X main.buildVersion=${version} -X main.buildCommit=${
              self.rev or "dirty"
            }"
          ];
        };
      };

      packages = { nix-cache-proxy, foo }@pkgs:
        pkgs // {
          defaultPackage = nix-cache-proxy;
        };

      hydraJobs = { nix-cache-proxy }@pkgs: pkgs;

      nixosModules.nix-cache-proxy = { };

      devShell = { devshell }: devshell.fromTOML ./devshell.toml;
    };
}
