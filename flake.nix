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

      forAllSystems =
        f:
        nixpkgs.lib.genAttrs systems (
          system:
          f {
            pkgs = import nixpkgs {
              inherit system;
            };
            inherit system self;
          }
        );
    in rec {
      packages = forAllSystems (args: import ./packages.nix args);

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
              go_1_26
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
