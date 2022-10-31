{
  buildGo118Module,
  lzma,
  pkg-config,
  inclusive,
  rev,
}: let
  final = package "sha256-GhyuW2LahbS9zwcN3OXrcfQBS/3EjZTHINN8jZ26qOo=";
  package = vendorSha256:
    buildGo118Module rec {
      pname = "spongix";
      version = "2022.10.18.001";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./testdata
        ./go.mod
        ./go.sum

        ./assemble.go
        ./assemble_test.go
        ./cache.go
        ./fake.go
        ./gc.go
        ./helpers.go
        ./log_record.go
        ./main.go
        ./narinfo.go
        ./narinfo_test.go
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
