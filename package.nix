{ buildGoModule, inclusive, rev }:
buildGoModule rec {
  pname = "nix-cache-proxy";
  version = "2022.02.17.001";
  vendorSha256 = "sha256-wUfj/ba+7RLy8xPsc9ClaeA0oh85DZdsipQEErF7lHg=";

  src = inclusive ./. [
    ./fixtures
    ./go.mod
    ./go.sum

    ./db.go
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

  ldflags =
    [ "-s" "-w" "-X main.buildVersion=${version} -X main.buildCommit=${rev}" ];
}
