{
  buildGo118Module,
  lzma,
  pkg-config,
  inclusive,
  rev,
}: let
  final = package "sha256-NGQZqIawCOb1UPjFCSkSfPV02jMOD+MUx6b5LZyzy94=";
  package = vendorSha256:
    buildGo118Module rec {
      pname = "spongix";
      version = "2022.05.10.001";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./testdata
        ./go.mod
        ./go.sum

        ./assemble.go
        ./assemble_test.go
        ./blob_manager.go
        ./cache.go
        ./docker.go
        ./docker_test.go
        ./fake.go
        ./gc.go
        ./helpers.go
        ./log_record.go
        ./main.go
        ./manifest_manager.go
        ./narinfo.go
        ./narinfo_test.go
        ./router.go
        ./router_test.go
        ./upload_manager.go
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
