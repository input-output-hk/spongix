{
  lib,
  buildGoModule,
  rev,
  symlinkBin,
}: let
  # TODO: split into multiple packages for each command.
  final = package "sha256-DNmG0Zhkr/NDye5IP52FbYwDyepXLvrivg2kcVFxehQ=";
  package = vendorHash:
    buildGoModule rec {
      pname = "spongix";
      version = "2025.01.01.001";
      inherit vendorHash;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = lib.fileset.toSource {
        root = ./.;
        fileset = lib.fileset.unions [
          ./testdata
          ./go.mod
          ./go.sum

          ./cmd
          ./pkg
          ./cache.go
          ./fake.go
          ./log_record.go
          ./main.go
          ./metrics.go
          ./registry.go
          ./registry_flake.go
          ./registry_handlers.go
          ./registry_test.go
          ./router.go
          ./router_test.go
        ];
      };

      proxyVendor = true;
      env.CGO_ENABLED = "0";

      ldflags = [
        "-s"
        "-w"
        "-X main.buildVersion=${version} -X main.buildCommit=${rev}"
        "-X main.symlinkBinStorePath=${symlinkBin}"
      ];
    };
in
  final
