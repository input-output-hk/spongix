{
  lib,
  buildGoModule,
  symlinkBin,
}: let
  # TODO: split into multiple packages for each command.
  final = package "sha256-tXGraMNI2VmtretUcm4yrcbPvIEvg4LVXPjUJJnHb94=";
  package = vendorHash:
    buildGoModule {
      pname = "spongix";
      version = "2026.04.02.001";
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

      env.CGO_ENABLED = "0";

      ldflags = [
        "-s"
        "-w"
        "-X main.symlinkBinStorePath=${symlinkBin}"
      ];
    };
in
  final
