{ buildGoModule, inclusive, rev }:
let
  final = package "sha256-Z7pTznhsyKQLkemTx7dM9V6/Leva88XfQM81yl3yPnE=";
  package = vendorSha256:
    buildGoModule rec {
      pname = "spongix";
      version = "2022.02.22.005";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./fixtures
        ./go.mod
        ./go.sum

        ./actions.go
        ./assemble.go
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

      CGO_ENABLED = "1";

      ldflags = [
        "-s"
        "-w"
        "-X main.buildVersion=${version} -X main.buildCommit=${rev}"
      ];
    };
in final
