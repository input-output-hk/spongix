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

  mapS3 = prefix: name: value: {
    url = value.url;
    region = value.region;
    profile = value.profile;
    credentials_file = "$CREDENTIALS_DIRECTORY/${prefix}";
    # value.credentialsFile
  };

  mapChunks = name: value: {
    minimum_size = value.minimumSize;
    average_size = value.averageSize;
    maximum_size = value.maximumSize;
    s3 = builtins.mapAttrs (mapS3 "chunks") value.s3;
  };

  mapNamespace = name: value: {
    substituters = value.substituters;
    trusted_public_keys = value.trustedPublicKeys;
    cache_info_priority = value.cacheInfoPriority;
    s3 = builtins.mapAttrs (mapS3 "namespace_${name}") value.s3;
  };

  configFile = pkgs.writeTextFile {
    name = "spongix.json";
    text = builtins.toJSON {
      dir = cfg.cacheDir;
      listen = "${cfg.host}:${builtins.toString cfg.port}";
      log_level = cfg.logLevel;
      log_mode = cfg.logMode;
      namespaces = builtins.mapAttrs mapNamespace cfg.namespaces;
      chunks = builtins.mapAttrs mapChunks cfg.chunks;
    };
  };

  s3Type = lib.mkOption {
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
              type = lib.types.ints.between 48 4294967296;
              default = (1024 * 64) / 4;
              description = ''
                Minimum chunk size
              '';
            };

            averageSize = lib.mkOption {
              type = lib.types.ints.between 48 4294967296;
              default = 1024 * 64;
              description = ''
                Average chunk size
              '';
            };

            maximumSize = lib.mkOption {
              type = lib.types.ints.between 48 4294967296;
              default = (1024 * 64) * 4;
              description = ''
                Maximum chunk size
              '';
            };

            s3 = lib.mkOption {
              type = s3Type;
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

            s3 = lib.mkOption {
              type = s3Type;
            };
          };
        }));
        default = {};
      };

      # gc = lib.mkOption {
      #   type = lib.types.submodule {
      #     options = {
      #       enable = lib.mkEnableOption "Spongix garbage collection";

      #       cacheSize = lib.mkOption {
      #         type = lib.types.ints.positive;
      #         default = 10;
      #         description = ''
      #           Number of GB to keep in the local cache
      #         '';
      #       };

      #       interval = lib.mkOption {
      #         type = lib.types.str;
      #         default = "daily";
      #         description = ''
      #           Time between garbage collections of local store files (fast)
      #         '';
      #       };

      #       cacheDir = lib.mkOption {
      #         type = lib.types.str;
      #         default = cfg.cacheDir;
      #         description = ''
      #           Keep all cache state in this directory.
      #         '';
      #       };

      #       host = lib.mkOption {
      #         type = lib.types.str;
      #         default = cfg.host;
      #         description = ''
      #           Listen on this host. Will be 0.0.0.0 if empty.
      #         '';
      #       };

      #       port = lib.mkOption {
      #         type = lib.types.port;
      #         default = 7746;
      #         description = ''
      #           Listen on this port.
      #         '';
      #       };

      #       logLevel = lib.mkOption {
      #         type = lib.types.enum logLevels;
      #         default = "info";
      #       };

      #       logMode = lib.mkOption {
      #         type = lib.types.enum logModes;
      #         default = "production";
      #         description = ''
      #           production mode uses JSON formatting, while development mode is more
      #           human readable.
      #         '';
      #       };
      #     };
      #   };
      #   default = {};
      # };

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
        execStart = pkgs.writeShellApplication {
          name = "spongix-wrapper";
          runtimeInputs = [pkgs.jq];
          text = ''
            jq < ${cfg.configFile} > config.json
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
          ["chunks:${cfg.chunks.s3.credentialsFile}"]
          ++ (lib.mapAttrsToList (name: value: "${name}:${value.credentialsFile}")
            cfg.namespaces);
        ReadWritePaths = cfg.cacheDir;
        Restart = "on-failure";
        RestartSec = 5;
        OOMScoreAdjust = 1000;
        MemoryAccounting = "true";
        MemoryMax = "70%";
      };
    };

    # systemd.timers.spongix-gc = lib.mkIf cfg.gc.enable {
    #   wantedBy = ["timers.target"];
    #   timerConfig = {
    #     Persistent = true;
    #     Unit = "spongix-gc.service";
    #   };
    # };

    # systemd.services.spongix-gc = lib.mkIf cfg.gc.enable {
    #   wantedBy = ["multi-user.target"];

    #   environment = {
    #     CACHE_DIR = cfg.gc.cacheDir;
    #     LISTEN_ADDR = "${cfg.gc.host}:${toString cfg.gc.port}";
    #     CACHE_SIZE = toString cfg.gc.cacheSize;
    #     GC_INTERVAL = cfg.gc.interval;
    #     LOG_LEVEL = cfg.gc.logLevel;
    #     LOG_MODE = cfg.gc.logMode;
    #   };

    #   startAt = cfg.gc.interval;
    #   serviceConfig = {
    #     ExecStart = "${cfg.package}/bin/gc";
    #     Type = "oneshot";
    #     User = "spongix";
    #     Group = "spongix";
    #     DynamicUser = true;
    #     RemainAfterExit = true;
    #     StateDirectory = "spongix";
    #     WorkingDirectory = cfg.gc.cacheDir;
    #     ReadWritePaths = cfg.gc.cacheDir;
    #     Restart = "on-failure";
    #     RestartSec = "60s";
    #   };
    # };
  };
}
