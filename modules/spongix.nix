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

  storeTypes = ["pebble" "s3"];

  mapS3 = prefix: value: {
    url = value.url;
    region = value.region;
    profile = value.profile;
    credentials_file = "$CREDENTIALS_DIRECTORY/${prefix}";
  };

  mapNamespace = name: value:
    {
      substituters = value.substituters;
      cache_info_priority = value.cacheInfoPriority;
      store_type = value.storeType;
    }
    // lib.optionalAttrs (value.storeType == "s3") {
      s3 = mapS3 "namespace_${name}" value.s3;
    };

  configFile = builtins.toFile "spongix.json" (builtins.toJSON ({
      listen = "${cfg.host}:${builtins.toString cfg.port}";
      log_level = cfg.logLevel;
      log_mode = cfg.logMode;
      namespaces = builtins.mapAttrs mapNamespace cfg.namespaces;
      chunks =
        {
          minimum_size = cfg.chunks.minimumSize;
          average_size = cfg.chunks.averageSize;
          maximum_size = cfg.chunks.maximumSize;
          threads = cfg.chunks.threads;
          store_type = cfg.chunks.storeType;
        }
        // lib.optionalAttrs (cfg.chunks.storeType == "s3") {
          s3 = mapS3 "chunks" cfg.chunks.s3;
        }
        // lib.optionalAttrs (cfg.chunks.storeType == "pebble") {
          pebble_dir = cfg.chunks.pebbleDir;
        };
    }
    // lib.optionalAttrs (cfg.signingKeyFile != null) {
      signing_key_file = "$CREDENTIALS_DIRECTORY/signing-key";
    }));

  s3Option = lib.mkOption {
    type = lib.types.nullOr (lib.types.submodule {
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
    });
    default = null;
  };

  # Collect LoadCredential entries only for S3-backed chunks/namespaces
  s3Credentials =
    lib.optional (cfg.chunks.storeType == "s3") "chunks:${cfg.chunks.s3.credentialsFile}"
    ++ lib.mapAttrsToList (name: value: "namespace_${name}:${value.s3.credentialsFile}")
    (lib.filterAttrs (_: ns: ns.storeType == "s3") cfg.namespaces)
    ++ lib.optional (cfg.signingKeyFile != null) "signing-key:${cfg.signingKeyFile}";
in {
  options = {
    services.spongix = {
      enable = lib.mkEnableOption "Spongix";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.spongix;
      };

      chunks = lib.mkOption {
        default = {};
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

            storeType = lib.mkOption {
              type = lib.types.enum storeTypes;
              default = "pebble";
              description = ''
                Storage backend for chunks: "pebble" (local) or "s3".
              '';
            };

            pebbleDir = lib.mkOption {
              type = lib.types.str;
              default = "/var/lib/spongix/db";
              description = ''
                Directory for the Pebble chunk database. Only used when storeType is "pebble".
              '';
            };

            threads = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 0;
              description = ''
                Number of goroutines for chunk hashing/compression during uploads.
                0 means use all available CPUs (runtime.NumCPU()).
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

            cacheInfoPriority = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 50;
              description = ''
                Priority in /nix-cache-info
              '';
            };

            storeType = lib.mkOption {
              type = lib.types.enum storeTypes;
              default = "pebble";
              description = ''
                Storage backend for this namespace's indices: "pebble" (local) or "s3".
              '';
            };

            s3 = s3Option;
          };
        }));
        default = {};
      };

      signingKeyFile = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Path to the Nix signing secret key file.
          Loaded via systemd LoadCredential for security.
        '';
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
    assertions =
      [
        {
          assertion = cfg.chunks.storeType == "s3" -> cfg.chunks.s3 != null;
          message = "services.spongix.chunks.s3 must be set when chunks.storeType is \"s3\"";
        }
      ]
      ++ lib.mapAttrsToList (name: ns: {
        assertion = ns.storeType == "s3" -> ns.s3 != null;
        message = "services.spongix.namespaces.${name}.s3 must be set when storeType is \"s3\"";
      })
      cfg.namespaces;

    systemd.services.spongix = {
      wantedBy = ["multi-user.target"];

      serviceConfig =
        {
          ExecStart = "${cfg.package}/bin/spongix --config ${cfg.configFile}";
          User = "spongix";
          Group = "spongix";
          DynamicUser = true;
          StateDirectory = "spongix";
          Restart = "on-failure";
          RestartSec = 5;
          OOMScoreAdjust = 1000;
          MemoryAccounting = "true";
          MemoryMax = "70%";
        }
        // lib.optionalAttrs (s3Credentials != []) {
          LoadCredential = s3Credentials;
        };
    };
  };
}
