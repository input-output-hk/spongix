{
  lib,
  buildGoModule,
  rev,
}: let
  # TODO: split into multiple packages for each command.
  final = package "sha256-zB08rlFT8hxAtMf4Ikif2aXe2cZWlZu93YKQw6CNwik=";
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
      ];
    };
in
  final
