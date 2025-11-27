{
  description = "webring";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs?ref=nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
  flake-utils.lib.eachDefaultSystem (system:
    let
      pkgs = nixpkgs.legacyPackages.${system};
      webring = pkgs.callPackage ./nix/package.nix {};
    in
    {
      devShells.default = pkgs.mkShell {
        packages = with pkgs; [
          go
          postgresql
          gnumake
          go-migrate.overrideAttrs(oldAttrs: {
            tags = ["postgres"];
          })
        ];

        shellHook = ''
          ${pkgs.go}/bin/go mod tidy
        '';
      };

      apps.default = { type = "app"; program = "${webring}/bin/webring-server"; };
      apps.webring = self.apps.${system}.default;

      packages = {
        inherit webring;
        default = webring;
      };
    }) // {
      overlays.default = final: prev: {
        webring = prev.callPackage ./nix/package.nix {};
      };
      nixosModules.default = { imports = [./nix/module.nix]; };
    };
}
