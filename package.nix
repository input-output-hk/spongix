{
  buildGo120Module,
  lzma,
  pkg-config,
  inclusive,
  rev,
}: let
  # TODO: split into multiple packages for each command.
  final = package "sha256-QFhmWjzbOyjl0FQmfwJZecVhsZMM5G/B9inADxemurM=";
  package = vendorSha256:
    buildGo120Module rec {
      pname = "spongix";
      version = "2023.07.05.001";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./testdata
        ./go.mod
        ./go.sum

        ./cmd
        ./pkg
        ./cache.go
        ./fake.go
        ./helpers.go
        ./log_record.go
        ./main.go
        ./router.go
        ./router_test.go
      ];

      proxyVendor = true;
      CGO_ENABLED = "1";

      ldflags = [
        "-s"
        "-w"
        "-X main.buildVersion=${version} -X main.buildCommit=${rev}"
      ];
    };
in
  final
