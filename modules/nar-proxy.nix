{
  config,
  pkgs,
  lib,
  ...
}: let
  cfg = config.services.nar-proxy;
in {
  options = {
    services.nar-proxy = {
      enable = lib.mkEnableOption "nar-proxy";

      cacheUrl = lib.mkOption {
        type = lib.types.str;
        default = "https://cache.iog.io/";
      };

      prefix = lib.mkOption {
        type = lib.types.str;
        default = "/dl";
      };

      logLevel = lib.mkOption {
        type = lib.types.enum ["debug" "info" "warn" "error" "dpanic" "panic" "fatal"];
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

      host = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          Listen on this host. Will be 0.0.0.0 if empty.
        '';
      };

      port = lib.mkOption {
        type = lib.types.port;
        default = 7747;
        description = ''
          Listen on this port.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.nar-proxy = {
      wantedBy = ["multi-user.target"];

      serviceConfig = let
        args = lib.cli.toGNUCommandLine {} {
          cache-url = cfg.cacheUrl;
          prefix = cfg.prefix;
          log-level = cfg.logLevel;
          log-mode = cfg.logMode;
          listen = "${cfg.host}:${toString cfg.port}";
        };
      in {
        ExecStart = toString (["${pkgs.spongix}/bin/nar-proxy"] ++ args);
        User = "nar-proxy";
        Group = "nar-proxy";
        DynamicUser = true;
        StateDirectory = "nar-proxy";
        Restart = "on-failure";
        RestartSec = 5;
      };
    };
  };
}
