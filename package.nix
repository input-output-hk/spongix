{
  buildGoModule,
  inclusive,
  rev,
}: let
  final = package "sha256-wPHiDqvOib/pA/4hp4Z8GIW4SXM+iIKADjpDUr6Xa0A=";
  package = vendorSha256:
    buildGoModule rec {
      pname = "spongix";
      version = "2022.03.12.003";
      inherit vendorSha256;

      passthru.invalidHash =
        package "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

      src = inclusive ./. [
        ./testdata
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
        ./tee.go
      ];

      CGO_ENABLED = "1";

      ldflags = [
        "-s"
        "-w"
        "-X main.buildVersion=${version} -X main.buildCommit=${rev}"
      ];
    };
in
  final
