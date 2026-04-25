{
  description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  };

  outputs =
    {
      nixpkgs,
      ...
    }:
    let
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-darwin"
        "x86_64-linux"
      ];

      forAllSystems =
        f: overlays: nixpkgs.lib.genAttrs systems (system: f (import nixpkgs { inherit system overlays; }));

      overlay = import ./overlay.nix;
    in
    {
      icu = nixpkgs.icu77;
      packages = forAllSystems (import ./packages.nix) [ overlay ];
      overlay.default = overlay;

      apps = forAllSystems (
        { self, system, ... }:
        rec {
          bd = {
            type = "app";
            program = "${self.packages.${system}.default}/bin/bd";
          };
          default = bd;
        }
      );

      devShells = forAllSystems (
        { pkgs, ... }:
        {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              git
              gopls
              gotools
              golangci-lint
              sqlite
            ];
            shellHook = ''
              echo "beads development shell"
              echo "Go version: $(go version)"
            '';
          };
        }
      );
    };
}
