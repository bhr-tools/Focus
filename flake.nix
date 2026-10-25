{
  description = "Focus — Minimalist Pomodoro & Timetracker with Rosé Pine design";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      # Supported systems
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];

      # Package definitions by architecture
      systemOutputs = flake-utils.lib.eachSystem supportedSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          packages = rec {
            focus = pkgs.buildGoModule {
              pname = "focus";
              version = "1.0.0";
              src = ./.;

              subPackages = [ "cmd/focus" ];
              vendorHash = null;

              meta = with pkgs.lib; {
                description = "Focus Pomodoro & Timer GUI/CLI with Rosé Pine theme";
                homepage = "https://github.com/bhr-tools/Focus";
                license = licenses.mit;
                mainProgram = "focus";
              };
            };
            default = focus;
          };

          apps = rec {
            focus = flake-utils.lib.mkApp {
              drv = self.packages.${system}.focus;
              name = "focus";
            };
            default = focus;
          };

          devShells.default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              gopls
              sqlite
            ];
          };
        }
      );

      # Native multi-instance NixOS module for server / desktop
      nixosModule = { config, lib, pkgs, ... }:
        let
          cfg = config.services.focus;
        in
        {
          options.services.focus = {
            enable = lib.mkEnableOption "Focus Timetracker Server & Web Daemon";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
              description = "The Focus package to use.";
            };

            instances = lib.mkOption {
              type = lib.types.attrsOf (lib.types.submodule ({ name, config, ... }: {
                options = {
                  port = lib.mkOption {
                    type = lib.types.port;
                    description = "HTTP listening port for this Focus instance.";
                  };

                  host = lib.mkOption {
                    type = lib.types.str;
                    default = "127.0.0.1";
                    description = "IP address on which the instance listens.";
                  };

                  dataDir = lib.mkOption {
                    type = lib.types.path;
                    default = if name == "default" then "/var/lib/focus" else "/var/lib/focus-${name}";
                    description = "Data directory for the Focus instance.";
                  };

                  dbPath = lib.mkOption {
                    type = lib.types.path;
                    default = "${config.dataDir}/focus.db";
                    description = "Path to the SQLite database for this instance.";
                  };

                  user = lib.mkOption {
                    type = lib.types.str;
                    default = "focus";
                    description = "System user to run the instance under.";
                  };

                  group = lib.mkOption {
                    type = lib.types.str;
                    default = "focus";
                    description = "System group to run the instance under.";
                  };

                  openFirewall = lib.mkOption {
                    type = lib.types.bool;
                    default = false;
                    description = "Automatically open the port in the NixOS firewall.";
                  };
                };
              }));
              default = {};
              description = "Definition of independent Focus instances.";
            };
          };

          config = lib.mkIf (cfg.enable && cfg.instances != {}) {
            networking.firewall.allowedTCPPorts = lib.concatLists (
              lib.mapAttrsToList (_: inst: lib.optional inst.openFirewall inst.port) cfg.instances
            );

            users.users = lib.mkIf (lib.any (inst: inst.user == "focus") (lib.attrValues cfg.instances)) {
              focus = {
                isSystemUser = true;
                group = "focus";
                home = "/var/lib/focus";
                createHome = true;
                description = "Focus Timetracker daemon user";
              };
            };

            users.groups = lib.mkIf (lib.any (inst: inst.group == "focus") (lib.attrValues cfg.instances)) {
              focus = {};
            };

            systemd.services = lib.mapAttrs' (name: inst:
              let
                serviceName = if name == "default" then "focus" else "focus-${name}";
                stateDir = lib.removePrefix "/var/lib/" (toString inst.dataDir);
              in
              lib.nameValuePair serviceName {
                description = "Focus Pomodoro & Timetracker Daemon (${name})";
                after = [ "network.target" ];
                wantedBy = [ "multi-user.target" ];

                serviceConfig = {
                  ExecStart = "${cfg.package}/bin/focus daemon -host ${inst.host} -port ${toString inst.port} -db ${toString inst.dbPath}";
                  Restart = "always";
                  RestartSec = 3;
                  User = inst.user;
                  Group = inst.group;
                  StateDirectory = lib.mkIf (lib.hasPrefix "/var/lib/" (toString inst.dataDir)) stateDir;
                  WorkingDirectory = toString inst.dataDir;
                  NoNewPrivileges = true;
                };
              }
            ) cfg.instances;
          };
        };
    in
    systemOutputs // {
      nixosModules.default = nixosModule;
      nixosModules.focus = nixosModule;
      overlays.default = final: prev: {
        focus = self.packages.${final.stdenv.hostPlatform.system}.default;
      };
    };
}
