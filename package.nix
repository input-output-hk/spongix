{
  buildGo118Module,
  lzma,
  pkg-config,
  inclusive,
  rev,
}: let
  # TODO: split into multiple packages for each command.
  final = package "sha256-o2DiTC2sRZ33LY+hfH9tfFwkzi+JySS6+d/ibBdl6q0=";
  package = vendorSha256:
    buildGo118Module rec {
      pname = "spongix";
      version = "2022.11.30.001";
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
