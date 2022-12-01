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

  mapNamespace = name: value: {
    substituters = value.substituters;
    secret_key_file = "__${name}__SECRET__KEY__";
    trusted_public_keys = value.trustedPublicKeys;
    cache_info_priority = value.cacheInfoPriority;
  };

  configFile = pkgs.writeTextFile {
    name = "spongix.json";
    text = builtins.toJSON {
      dir = cfg.cacheDir;
      listen = "${cfg.host}:${builtins.toString cfg.port}";
      log_level = cfg.logLevel;
      log_mode = cfg.logMode;
      average_chunk_size = cfg.averageChunkSize;
      s3_bucket_url = cfg.bucketURL;
      s3_bucket_region = cfg.bucketRegion;
      namespaces = builtins.mapAttrs mapNamespace cfg.namespaces;
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

      averageChunkSize = lib.mkOption {
        type = lib.types.ints.between 48 4294967296;
        default = 65536;
        description = ''
          Chunk size will be between /4 and *4 of this value
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
            secretKeyFile = lib.mkOption {
              type = lib.types.str;
              default = null;
              description = ''
                Path to a file containing the private key used for signing narinfos.
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
          };
        }));
        default = {};
      };

      gc = lib.mkOption {
        type = lib.types.submodule {
          options = {
            enable = lib.mkEnableOption "Spongix garbage collection";

            cacheSize = lib.mkOption {
              type = lib.types.ints.positive;
              default = 10;
              description = ''
                Number of GB to keep in the local cache
              '';
            };

            interval = lib.mkOption {
              type = lib.types.str;
              default = "daily";
              description = ''
                Time between garbage collections of local store files (fast)
              '';
            };

            cacheDir = lib.mkOption {
              type = lib.types.str;
              default = cfg.cacheDir;
              description = ''
                Keep all cache state in this directory.
              '';
            };

            host = lib.mkOption {
              type = lib.types.str;
              default = cfg.host;
              description = ''
                Listen on this host. Will be 0.0.0.0 if empty.
              '';
            };

            port = lib.mkOption {
              type = lib.types.port;
              default = 7746;
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
          };
        };
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

      serviceConfig = let
        mapJqArg = name: value: ''--arg ${name} "$CREDENTIALS_DIRECTORY/${name}"'';
        args = builtins.concatStringsSep " | " (lib.mapAttrsToList mapJqArg cfg.namespaces);
        mapJqQuery = name: value: ''.namespaces["${name}"].secret_key_file = ''$${name}'';
        jqQuery = builtins.concatStringsSep " | " (lib.mapAttrsToList mapJqQuery cfg.namespaces);
        execStart = pkgs.writeShellApplication {
          name = "spongix-wrapper";
          runtimeInputs = [pkgs.jq];
          text = ''
            jq < ${cfg.configFile} ${args} '${jqQuery}' > config.json
            exec ${cfg.package}/bin/spongix
          '';
        };
      in {
        ExecStart = "${execStart}/bin/spongix-wrapper";
        User = "spongix";
        Group = "spongix";
        DynamicUser = true;
        StateDirectory = "spongix";
        WorkingDirectory = cfg.cacheDir;
        LoadCredential =
          lib.mapAttrsToList (name: value: "${name}:${value.secretKeyFile}")
          cfg.namespaces;
        ReadWritePaths = cfg.cacheDir;
        Restart = "on-failure";
        RestartSec = 5;
        OOMScoreAdjust = 1000;
        MemoryAccounting = "true";
        MemoryMax = "70%";
      };
    };

    systemd.timers.spongix-gc = lib.mkIf cfg.gc.enable {
      wantedBy = ["timers.target"];
      timerConfig = {
        Persistent = true;
        Unit = "spongix-gc.service";
      };
    };

    systemd.services.spongix-gc = lib.mkIf cfg.gc.enable {
      wantedBy = ["multi-user.target"];

      environment = {
        CACHE_DIR = cfg.gc.cacheDir;
        LISTEN_ADDR = "${cfg.gc.host}:${toString cfg.gc.port}";
        CACHE_SIZE = toString cfg.gc.cacheSize;
        GC_INTERVAL = cfg.gc.interval;
        LOG_LEVEL = cfg.gc.logLevel;
        LOG_MODE = cfg.gc.logMode;
      };

      startAt = cfg.gc.interval;
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/gc";
        Type = "oneshot";
        User = "spongix";
        Group = "spongix";
        DynamicUser = true;
        RemainAfterExit = true;
        StateDirectory = "spongix";
        WorkingDirectory = cfg.gc.cacheDir;
        ReadWritePaths = cfg.gc.cacheDir;
        Restart = "on-failure";
        RestartSec = "60s";
      };
    };
  };
}
