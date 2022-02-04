{ config, lib, pkgs, ... }:
let cfg = config.services.nix-cache-proxy;
in {
  options = {
    services.nix-cache-proxy = {
      enable = lib.mkEnableOption "Enable the Nix Cache Proxy";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.nix-cache-proxy;
      };

      awsBucketName = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
      };

      awsBucketRegion = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
      };

      awsProfile = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
      };

      cacheDir = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/private/nix-cache-proxy/cache";
      };

      host = lib.mkOption {
        type = lib.types.str;
        default = "";
      };

      port = lib.mkOption {
        type = lib.types.port;
        default = 7745;
      };

      secretKeyFiles = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
      };

      substituters = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
      };

      trustedPublicKeys = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
      };
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.nix-cache-proxy = {
      wantedBy = [ "multi-user.target" ];

      path = [ config.nix.package ];

      environment = {
        AWS_BUCKET_NAME = cfg.awsBucketName;
        AWS_BUCKET_REGION = cfg.awsBucketRegion;
        AWS_PROFILE = cfg.awsProfile;
        CACHE_DIR = cfg.cacheDir;
        LISTEN_ADDR = "${cfg.host}:${toString cfg.port}";
        NIX_SUBSTITUTERS = lib.concatStringsSep "," cfg.substituters;
        NIX_TRUSTED_PUBLIC_KEYS =
          lib.concatStringsSep "," cfg.trustedPublicKeys;
      };

      script = ''
        set -exuo pipefail
        export NIX_SECRET_KEY_FILES="${
          lib.concatStringsSep ","
          (lib.mapAttrsToList (name: value: "$CREDENTIALS_DIRECTORY/${name}")
            cfg.secretKeyFiles)
        }"
        exec "${cfg.package}/bin/nix-cache-proxy"
      '';

      serviceConfig = {
        User = "nix-cache-proxy";
        Group = "nix-cache-proxy";
        DynamicUser = true;
        StateDirectory = "nix-cache-proxy";
        WorkingDirectory = "/var/lib/private/nix-cache-proxy";
        LoadCredential = lib.mapAttrsToList (name: value: "${name}:${value}")
          cfg.secretKeyFiles;
        ReadWritePaths = cfg.cacheDir;
      };
    };
  };
}
