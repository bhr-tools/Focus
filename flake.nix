{
  description = "Focus — minimalist Pomodoro & time tracker with Rosé Pine design";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    kit = {
      url = "github:bhr-tools/kit";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, kit }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        # focus-server (the web app and its API) and focus (the command line
        # of status bars, which drives a server).
        focus = pkgs.buildGoModule {
          pname = "focus";
          version = "2.0.0";
          src = ./.;

          subPackages = [ "cmd/focus" "cmd/focus-server" ];
          vendorHash = null;

          meta = with pkgs.lib; {
            description = "Minimalist Pomodoro & time tracker with Rosé Pine design";
            homepage = "https://github.com/bhr-tools/Focus";
            license = licenses.mit;
            mainProgram = "focus";
          };
        };
        default = focus;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gopls sqlite nodejs ];
        };
      });

      nixosModules.default = kit.lib.mkAppModule {
        name = "focus";
        title = "Focus";
        package = pkgs: self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        dbFile = "focus.db";
        bin = "focus-server";
      };
      nixosModules.focus = self.nixosModules.default;

      overlays.default = final: prev: {
        focus = self.packages.${final.stdenv.hostPlatform.system}.default;
      };
    };
}
