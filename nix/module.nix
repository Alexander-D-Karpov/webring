{ config, lib, pkgs, ... }: let
  webring = pkgs.callPackage ./package.nix {};
  cfg = config.services.webring;
  defaultUser = "webring";
  defaultGroup = "webring";
  migrate = pkgs.go-migrate.overrideAttrs(oldAttrs: {
    tags = ["postgres"];
  });
in with lib; {
  options.services.webring = {
    enable = mkEnableOption "webring service";

    port = mkOption {
      description = "Port the service will listen on.";
      default = 8000;
      type = types.port;
    };

    host = mkOption {
      description = "Hostname the service will be available on.";
      type = types.str;
      example = "webring.example.com";
    };

    database = {
      connectionString = mkOption {
        description = "PostgreSQL database connection string.";
        type = types.str;
        default = "";
      };
      createLocally = mkOption {
        description = "Create the database and database user locally.";
        type = types.bool;
        default = true;
      };
      migrate = mkOption {
        description = "Whether or not to automatically run migrations on startup.";
        type = types.bool;
        default = true;
      };
    };

    environmentFile = mkOption {
      description = "Path to a .env with runtime secrets.";
      type = lib.types.nullOr lib.types.path;
      default = null;
      example = ''
        Path to a file containing extra config options in the systemd `EnvironmentFile`
        format. Refer to the .env.template file for config options.

        This can be used to pass secrets to webring server without putting them in the Nix store.
      '';
    };

    user = mkOption {
      description = "User account under which webring service runs.";
      default = defaultUser;
      type = types.str;
    };

    group = mkOption {
      description = "Group under which webring service runs.";
      default = defaultGroup;
      type = types.str;
    };
  };

  config = let
    database =
      if cfg.database.createLocally then
        "postgres:///${cfg.user}?host=/run/postgresql"
      else
        cfg.database.connectionString;
  in mkIf cfg.enable {
    users.users = mkIf (cfg.user == defaultUser) {
      webring = {
        description = "webring service user";
        isSystemUser = true;
        group = cfg.group;
        home = "/var/lib/webring";
        createHome = true;
      };
    };

    users.groups = mkIf (cfg.group == defaultGroup) {
      webring = { };
    };

    services.postgresql = mkIf cfg.database.createLocally {
      enable = true;
      ensureDatabases = [ cfg.user ];
      ensureUsers = [
        {
          name = cfg.user;
          ensureDBOwnership = true;
        }
      ];
    };

    systemd.services.webring = {
      description = "webring service";
      after = [ "network.target" ]
        ++ optionals cfg.database.createLocally [ "postgresql.target" ]
        ++ optionals cfg.database.migrate [ "webring-migration.service" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        PORT = toString cfg.port;
        DB_CONNECTION_STRING = database;
      };
      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = "/var/lib/webring";
        ExecStart = "${webring}/bin/webring-server";
        Restart = "on-failure";
        EnvironmentFile = lib.mkIf (cfg.environmentFile != null) cfg.environmentFile;
      };
    };

    systemd.services.webring-migration = mkIf cfg.database.migrate {
      description = "webring db migrations";
      before = [ "webring.service" ];
      after = optionals cfg.database.createLocally [ "postgresql.target" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = "/var/lib/webring";
        ExecStart = "${migrate}/bin/migrate -path ${../migrations} -database \"${database}\" up";
      };
    };
  };
}
