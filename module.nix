{ config, lib, pkgs, ... }:
let cfg = config.services.spongix;
in {
  options = {
    services.spongix = {
      enable = lib.mkEnableOption "Enable the Nix Cache Proxy";

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.spongix;
      };

	bucketURL =lib.mkOption{type = lib.types.nullOr lib.types.str; default=null;}; #        string        `arg:"--bucket-url,env:BUCKET_URL" help:"Bucket URL like s3+http://127.0.0.1:9000/ncp"`
	bucketRegion =lib.mkOption{type=lib.types.nullOr lib.types.str;default=null;}; #     string        `arg:"--bucket-region,env:BUCKET_REGION" help:"Region the bucket is in"`
	dir =lib.mkOption{type=lib.types.str;default="/var/lib/spongix";}; #              string        `arg:"--dir,env:CACHE_DIR" help:"directory for the cache"`
	listen =lib.mkOption{type=lib.types.str;default="";}; #            string        `arg:"--listen,env:LISTEN_ADDR" help:"Listen on this address"`
	secretKeyFiles =lib.mkOption{type=lib.types.attrsOf lib.types.str; default= {};}; #    []string      `arg:"--secret-key-files,required,env:NIX_SECRET_KEY_FILES" help:"Files containing your private nix signing keys"`
	substituters =lib.mkOption{type=lib.types.listOf lib.types.str; default=[];}; #      []string      `arg:"--substituters,env:NIX_SUBSTITUTERS"`
	trustedPublicKeys = lib.mkOption{type=lib.types.listOf lib.types.str; default=[];}; # []string      `arg:"--trusted-public-keys,env:NIX_TRUSTED_PUBLIC_KEYS"`
	cacheInfoPriority = {type=lib.types.ints.unsigned; default=50; }; # uint64        `arg:"--cache-info-priority,env:CACHE_INFO_PRIORITY" help:"Priority in nix-cache-info"`
	databaseDSN       = {type=lib.types.; default=; }; # string        `arg:"--database,env:DATABASE_DSN" help:"DSN for the db, like file:test.db"`
	averageChunkSize  = {type=lib.types.; default=; }; # uint64        `arg:"--average-chunk-size,env:AVERAGE_CHUNK_SIZE" help:"Chunk size will be between /4 and *4 of this value"`
	cacheSize         = {type=lib.types.; default=; }; # uint64        `arg:"--cache-size,env:CACHE_SIZE" help:"Number of gigabytes to keep in the disk cache"`
	verifyInterval    = {type=lib.types.; default=; }; # time.Duration `arg:"--verify-interval,env:VERIFY_INTERVAL" help:"Seconds between verification runs"`
	gcInterval        = {type=lib.types.; default=; }; # time.Duration `arg:"--gc-interval,env:GC_INTERVAL" help:"Seconds between store garbage collection runs"`
	logLevel          = {type=lib.types.; default=; }; # string        `arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	logMode           = {type=lib.types.; default=; }; # string        `arg:"--log-mode,env:LOG_MODE" help:"development or production"`



      bucketUrl = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
      };

      bucketRegion = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
      };

      dir = lib.mkOption {
        type = lib.types.str;
        default = "/var/lib/spongix";
      };

      localCacheSize = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 10;
        description = "Disk cache size in GB";
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
    systemd.services.spongix = {
      wantedBy = [ "multi-user.target" ];

      path = [ config.nix.package ];

      environment = {
        AWS_BUCKET_NAME = cfg.awsBucketName;
        AWS_BUCKET_REGION = cfg.awsBucketRegion;
        AWS_PROFILE = cfg.awsProfile;
        CACHE_DIR = cfg.cacheDir;
        CACHE_SIZE = toString cfg.cacheSize;
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
        exec "${cfg.package}/bin/spongix"
      '';

      serviceConfig = {
        User = "spongix";
        Group = "spongix";
        DynamicUser = true;
        StateDirectory = "spongix";
        WorkingDirectory = "/var/lib/spongix";
        LoadCredential = lib.mapAttrsToList (name: value: "${name}:${value}")
          cfg.secretKeyFiles;
        ReadWritePaths = cfg.cacheDir;
      };
    };
  };
}
