{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.spongix;
  join = lib.concatStringsSep ",";
in {
  options = {
    services.spongix = {
      enable = lib.mkEnableOption "Enable the Nix Cache Proxy";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.spongix;
      };

      bucketURL = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "URL of the S3 Bucket.";
        example = "s3+http://127.0.0.1:7745/spongix";
      };

      bucketRegion = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Region of the S3 bucket. (Also required for Minio)";
      };

      cacheDir = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/spongix";
        description = ''
          Keep all cache state in this directory.
        '';
      };

      host = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          Listen on this host. Will be 0.0.0.0 if empty.
        '';
      };

      port = lib.mkOption {
        type = lib.types.port;
        default = 7745;
        description = ''
          Listen on this port.
        '';
      };

      secretKeyFiles = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = {};
        description = ''
          An attrset of { name = path; } to files containing private keys used
          for signing narinfos.
          They may be located anywhere and will be made available by systemd.
          To generate a key, you can use
          `nix key generate-secret --key-name foo > foo.private`
        '';
      };

      substituters = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = ["https://cache.nixos.org"];
        description = ''
          Remote Nix caches
        '';
      };

      trustedPublicKeys = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = ["cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="];
        description = ''
          Keys in this list are kept in narinfo files, and re-signed with the spongix key.
          This should include the public key of your secret key.
          To generate a public key from the secret, you can use
          `nix key convert-secret-to-public < foo.private > foo.public`
        '';
      };

      cacheInfoPriority = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 50;
        description = ''
          Priority in /nix-cache-info
        '';
      };

      averageChunkSize = lib.mkOption {
        type = lib.types.ints.between 48 4294967296;
        default = 65536;
        description = ''
          Chunk size will be between /4 and *4 of this value
        '';
      };

      cacheSize = lib.mkOption {
        type = lib.types.ints.positive;
        default = 10;
        description = ''
          Number of GB to keep in the local cache
        '';
      };

      verifyInterval = lib.mkOption {
        type = lib.types.str;
        default = "24h";
        description = ''
          Time between verifcations of local store file integrity (slow and I/O intensive)
        '';
      };

      gcInterval = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = ''
          Time between garbage collections of local store files (fast)
        '';
      };

      logLevel = lib.mkOption {
        type = lib.types.enum [
          "debug"
          "info"
          "warn"
          "error"
          "dpanic"
          "panic"
          "fatal"
        ];
        default = "info";
      };

      logMode = lib.mkOption {
        type = lib.types.enum ["production" "development"];
        default = "production";
        description = ''
          production mode uses JSON formatting, while development mode is more
          human readable.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.spongix = {
      wantedBy = ["multi-user.target"];

      path = [config.nix.package];

      environment = {
        BUCKET_URL = cfg.bucketURL;
        BUCKET_REGION = cfg.bucketRegion;
        CACHE_DIR = cfg.cacheDir;
        LISTEN_ADDR = "${cfg.host}:${toString cfg.port}";
        NIX_SUBSTITUTERS = join cfg.substituters;
        NIX_TRUSTED_PUBLIC_KEYS = join cfg.trustedPublicKeys;
        CACHE_INFO_PRIORITY = toString cfg.cacheInfoPriority;
        AVERAGE_CHUNK_SIZE = toString cfg.averageChunkSize;
        CACHE_SIZE = toString cfg.cacheSize;
        VERIFY_INTERVAL = cfg.verifyInterval;
        GC_INTERVAL = cfg.gcInterval;
        LOG_LEVEL = cfg.logLevel;
        LOG_MODE = cfg.logMode;
      };

      script = ''
        set -exuo pipefail
        export NIX_SECRET_KEY_FILES="${
          join
          (lib.mapAttrsToList (name: value: "$CREDENTIALS_DIRECTORY/${name}")
            cfg.secretKeyFiles)
        }"
        exec "${cfg.package}/bin/spongix"
      '';

      serviceConfig = {
        User = "spongix";
        Group = "spongix";
        DynamicUser = true;
        StateDirectory = "spongix";
        WorkingDirectory = cfg.cacheDir;
        LoadCredential =
          lib.mapAttrsToList (name: value: "${name}:${value}")
          cfg.secretKeyFiles;
        ReadWritePaths = cfg.cacheDir;
        Restart = "on-failure";
        RestartSec = 5;
      };
    };
  };
}
