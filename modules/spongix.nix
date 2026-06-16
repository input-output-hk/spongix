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
      pprof_listen = cfg.pprofListen;
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
          pebble_wal_dir = cfg.chunks.pebbleWALDir;
          pebble_memtable_size = cfg.chunks.pebbleMemTableSize;
          pebble_l0_compaction_threshold = cfg.chunks.pebbleL0CompactionThreshold;
          batch_max_bytes = cfg.chunks.batchMaxBytes;
        };
    }
    // lib.optionalAttrs (cfg.signingKeyFile != null) {
      signing_key_file = "$CREDENTIALS_DIRECTORY/signing-key";
    }
    // lib.optionalAttrs cfg.gc.enable {
      gc = {
        interval = cfg.gc.interval;
        retention_period = cfg.gc.retention;
        max_bytes = cfg.gc.maxBytes;
        pinned_hashes_file = cfg.gc.pinnedHashesFile;
        dry_run = cfg.gc.dryRun;
      };
    }
    // lib.optionalAttrs cfg.pinAPI.enable {
      pin_api = {
        enable = true;
        bearer_token = "$CREDENTIALS_DIRECTORY/pin-api-token";
      };
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
    ++ lib.optional (cfg.signingKeyFile != null) "signing-key:${cfg.signingKeyFile}"
    ++ lib.optional (cfg.pinAPI.enable && cfg.pinAPI.bearerTokenFile != null)
    "pin-api-token:${cfg.pinAPI.bearerTokenFile}";
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

            pebbleWALDir = lib.mkOption {
              type = lib.types.str;
              default = "";
              description = ''
                Separate WAL directory for Pebble (e.g. on SSD). Empty means same as pebbleDir.
              '';
            };

            pebbleMemTableSize = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 67108864;
              description = ''
                Pebble memtable size in bytes. Default 64MB.
              '';
            };

            pebbleL0CompactionThreshold = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 4;
              description = ''
                Pebble L0 compaction threshold. Default 4.
              '';
            };

            batchMaxBytes = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 268435456;
              description = ''
                Maximum bytes to buffer in memory before flushing chunks to Pebble. Default 256MB.
              '';
            };
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

      pprofListen = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "127.0.0.1:6060";
        description = ''
          Address for the pprof debug HTTP server (goroutine dumps, heap
          profiles). Empty string disables it. Should always be a loopback
          address; access remotely via SSH port forwarding.
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

      gc = lib.mkOption {
        default = {};
        type = lib.types.submodule {
          options = {
            enable = lib.mkEnableOption "periodic garbage collection";

            interval = lib.mkOption {
              type = lib.types.str;
              default = "24h";
              description = ''
                How often to run GC, as a Go duration string.
              '';
            };

            retention = lib.mkOption {
              type = lib.types.str;
              default = "720h";
              description = ''
                Retention period for narinfos. Narinfos newer than this are kept.
                Default 720h (30 days).
              '';
            };

            maxBytes = lib.mkOption {
              type = lib.types.ints.unsigned;
              default = 0;
              description = ''
                Maximum disk usage in bytes. When exceeded, oldest unpinned
                narinfos are evicted. 0 means no size limit.
              '';
            };

            pinnedHashesFile = lib.mkOption {
              type = lib.types.str;
              default = "/var/lib/spongix/pinned-hashes";
              description = ''
                Path to a file containing pinned store path hashes, one per line.
                Missing file is treated as empty.
              '';
            };

            dryRun = lib.mkOption {
              type = lib.types.bool;
              default = false;
              description = ''
                Only report what would be deleted, without actually deleting.
              '';
            };
          };
        };
      };

      pinAPI = lib.mkOption {
        default = {};
        type = lib.types.submodule {
          options = {
            enable = lib.mkEnableOption "HTTP pin management API";

            bearerTokenFile = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = ''
                Path to a file containing the bearer token for the pin API.
                Loaded via systemd LoadCredential to avoid secrets in the Nix store.
                Required when the pin API is enabled.
              '';
            };
          };
        };
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
        {
          assertion = cfg.pinAPI.enable -> cfg.pinAPI.bearerTokenFile != null;
          message = "services.spongix.pinAPI.bearerTokenFile is required when the pin API is enabled";
        }
      ]
      ++ lib.mapAttrsToList (name: ns: {
        assertion = ns.storeType == "s3" -> ns.s3 != null;
        message = "services.spongix.namespaces.${name}.s3 must be set when storeType is \"s3\"";
      })
      cfg.namespaces;

    environment.systemPackages = [pkgs.spongixctl];

    environment.variables = let
      nsNames = builtins.attrNames cfg.namespaces;
    in
      lib.optionalAttrs (builtins.length nsNames == 1) {
        SPONGIX_URL = "http://localhost:${toString cfg.port}/${builtins.head nsNames}";
      }
      // lib.optionalAttrs (cfg.pinAPI.enable && cfg.pinAPI.bearerTokenFile != null) {
        SPONGIX_TOKEN_FILE = cfg.pinAPI.bearerTokenFile;
      };

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
          # The Go server drains in-flight requests (up to 15 min HTTP
          # timeout) then drains worker pools and closes Pebble.  Give
          # it enough time to finish gracefully before systemd SIGKILLs.
          TimeoutStopSec = "16min";
          OOMScoreAdjust = 1000;
          MemoryAccounting = "true";
          MemoryMax = "70%";
        }
        // lib.optionalAttrs (s3Credentials != []) {
          LoadCredential = s3Credentials;
        }
        // lib.optionalAttrs (lib.hasPrefix "/run/spongix" cfg.chunks.pebbleWALDir) {
          RuntimeDirectory = lib.removePrefix "/run/" cfg.chunks.pebbleWALDir;
          RuntimeDirectoryPreserve = true;
        };
    };
  };
}
