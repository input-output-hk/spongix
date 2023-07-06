{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.spongix;

  logLevels = [
    "debug"
    "info"
    "warn"
    "error"
    "dpanic"
    "panic"
    "fatal"
  ];

  logModes = ["production" "development"];

  mapS3 = prefix: value: {
    url = value.url;
    region = value.region;
    profile = value.profile;
    credentials_file = "$CREDENTIALS_DIRECTORY/${prefix}";
  };

  mapNamespace = name: value: {
    substituters = value.substituters;
    trusted_public_keys = value.trustedPublicKeys;
    cache_info_priority = value.cacheInfoPriority;
    s3 = mapS3 "namespace_${name}" value.s3;
  };

  configFile = builtins.toFile "spongix.json" (builtins.toJSON {
    listen = "${cfg.host}:${builtins.toString cfg.port}";
    log_level = cfg.logLevel;
    log_mode = cfg.logMode;
    namespaces = builtins.mapAttrs mapNamespace cfg.namespaces;
    chunks = {
      minimum_size = cfg.chunks.minimumSize;
      average_size = cfg.chunks.averageSize;
      maximum_size = cfg.chunks.maximumSize;
      s3 = mapS3 "chunks" cfg.chunks.s3;
    };
  });

  s3Option = lib.mkOption {
    type = lib.types.submodule {
      options = {
        url = lib.mkOption {
          type = lib.types.str;
          description = ''
            URL of the S3 Bucket.
          '';
        };

        region = lib.mkOption {
          type = lib.types.str;
          description = ''
            Region of the S3 bucket. (Also required for Minio)
          '';
        };

        profile = lib.mkOption {
          type = lib.types.str;
          default = "default";
          description = ''
            Profile to use for the S3 bucket.
          '';
        };

        credentialsFile = lib.mkOption {
          type = lib.types.str;
          description = ''
            Path to a file containing the credentials for the S3 bucket.
          '';
        };
      };
    };
  };
in {
  options = {
    services.spongix = {
      enable = lib.mkEnableOption "Spongix";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.spongix;
      };

      chunks = lib.mkOption {
        type = lib.types.submodule {
          options = {
            minimumSize = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = (1024 * 64) / 4;
              description = ''
                Minimum chunk size
              '';
            };

            averageSize = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 1024 * 64;
              description = ''
                Average chunk size
              '';
            };

            maximumSize = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = (1024 * 64) * 4;
              description = ''
                Maximum chunk size
              '';
            };

            s3 = s3Option;
          };
        };
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

      logLevel = lib.mkOption {
        type = lib.types.enum logLevels;
        default = "info";
      };

      logMode = lib.mkOption {
        type = lib.types.enum logModes;
        default = "production";
        description = ''
          production mode uses JSON formatting, while development mode is more
          human readable.
        '';
      };

      namespaces = lib.mkOption {
        type = lib.types.attrsOf (lib.types.submodule ({name, ...}: {
          options = {
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

            s3 = s3Option;
          };
        }));
        default = {};
      };

      configFile = lib.mkOption {
        type = lib.types.path;
        default = builtins.toPath configFile;
        description = ''
          This is automatically generated based on the above options, and you
          can override it to fully control your configuration.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.spongix = {
      wantedBy = ["multi-user.target"];

      serviceConfig = {
        ExecStart = "${cfg.package}/bin/spongix --config ${cfg.configFile}";
        User = "spongix";
        Group = "spongix";
        DynamicUser = true;
        StateDirectory = "spongix";
        LoadCredential =
          ["chunks:${cfg.chunks.s3.credentialsFile}"]
          ++ (lib.mapAttrsToList (name: value: "namespace_${name}:${value.s3.credentialsFile}")
            cfg.namespaces);
        Restart = "on-failure";
        RestartSec = 5;
        OOMScoreAdjust = 1000;
        MemoryAccounting = "true";
        MemoryMax = "70%";
      };
    };
  };
}
