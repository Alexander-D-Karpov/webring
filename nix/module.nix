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

    ringCheck = {
      enable = mkOption {
        description = ''
          Run the ring integrity checker, which loads every member in a headless
          Chromium and records how well its webring widget works. The results are what
          the /health and /tiers pages show; without it those pages stay empty.
        '';
        default = true;
        type = types.bool;
      };

      interval = mkOption {
        description = ''
          How often to sweep the ring, as a systemd time span. A sweep visits every
          member in a real browser, so this is deliberately infrequent.
        '';
        default = "6h";
        type = types.str;
      };

      notify = mkOption {
        description = ''
          Send a Telegram message to the admin chat when a member's verdict changes.
          Off by default: switching the checker on for a ring that has never been
          measured would otherwise announce every member at once.
        '';
        default = false;
        type = types.bool;
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

    # The checker runs from a timer rather than as a long-lived service. A sweep is a
    # burst of work every few hours, so there is nothing for a resident process to do in
    # between, and systemd handles the schedule, the catch-up after downtime and the
    # jitter better than a sleep loop would.
    systemd.services.webring-ringcheck = mkIf cfg.ringCheck.enable {
      description = "webring ring integrity check";
      after = [ "network-online.target" ]
        ++ optionals cfg.database.createLocally [ "postgresql.target" ]
        ++ optionals cfg.database.migrate [ "webring-migration.service" ];
      wants = [ "network-online.target" ];
      environment = {
        DB_CONNECTION_STRING = database;
        HEALTH_NOTIFY = boolToString cfg.ringCheck.notify;
        # Chromium writes a profile on startup and refuses to run without somewhere to
        # put it.
        HOME = "/var/lib/webring";
      };
      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = "/var/lib/webring";
        ExecStart = "${webring}/bin/ringcheck -once";
        EnvironmentFile = lib.mkIf (cfg.environmentFile != null) cfg.environmentFile;
        # Every member is visited one at a time in a browser, so a full sweep of a
        # seventy-member ring runs to several minutes.
        TimeoutStartSec = "45min";
      };
    };

    systemd.timers.webring-ringcheck = mkIf cfg.ringCheck.enable {
      description = "webring ring integrity check schedule";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        # Let the machine settle before spending several minutes in a browser.
        OnBootSec = "10min";
        OnUnitActiveSec = cfg.ringCheck.interval;
        # Catch up after downtime rather than silently skipping a sweep.
        Persistent = true;
        # Members are other people's servers; do not knock at the same second every time.
        RandomizedDelaySec = "5min";
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
