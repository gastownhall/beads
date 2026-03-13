{
  description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  };

  outputs =
    {
      self,
      nixpkgs,
    }:
    let
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-darwin"
        "x86_64-linux"
      ];

      # Go 1.25.8: go.mod requires it, nixpkgs has 1.25.7
      # Remove when nixpkgs ships Go >= 1.25.8
      goOverlay = final: prev: {
        go = prev.go.overrideAttrs {
          version = "1.25.8";
          src = prev.fetchurl {
            url = "https://go.dev/dl/go1.25.8.src.tar.gz";
            hash = "sha256-6YjUokRqx/4/baoImljpk2pSo4E1Wt7ByJgyMKjWxZ4=";
          };
        };
      };

      forAllSystems =
        f:
        nixpkgs.lib.genAttrs systems (
          system:
          f {
            pkgs = import nixpkgs {
              inherit system;
              overlays = [ goOverlay ];
            };
            inherit system self;
          }
        );
    in
    {
      packages = forAllSystems (args: import ./packages.nix args);

      apps = forAllSystems (
        { self, system, ... }:
        {
          default = {
            type = "app";
            program = "${self.packages.${system}.default}/bin/bd";
          };
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
