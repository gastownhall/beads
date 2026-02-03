{
  description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachSystem
      [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ]
      (
        system:
        let
          # Create overlay for Go 1.25.6
          # nixpkgs-unstable has 1.25.1 but dolthub/driver requires 1.25.6
          goOverlay = final: prev: {
            go_1_25_6 = prev.go.overrideAttrs (oldAttrs: rec {
              version = "1.25.6";
              src = prev.fetchurl {
                url = "https://go.dev/dl/go${version}.src.tar.gz";
                hash = "sha256-WMv3ceRNdt5vVtGeM7d9dFoeSJNAkih15GWFuXXCsFk=";
              };
            });
          };
          pkgs = import nixpkgs {
            inherit system;
            overlays = [ goOverlay ];
          };
          # Override buildGoModule to use Go 1.25.6 for both main build AND go-modules derivation
          buildGoModule126 = pkgs.buildGoModule.override { go = pkgs.go_1_25_6; };
          bdBase = pkgs.callPackage ./default.nix { inherit pkgs self; buildGoModule = buildGoModule126; };
          # Wrap the base package with shell completions baked in
          bd = pkgs.stdenv.mkDerivation {
            pname = "beads";
            version = bdBase.version;

            phases = [ "installPhase" ];

            installPhase = ''
              mkdir -p $out/bin
              cp ${bdBase}/bin/bd $out/bin/bd

              # Generate shell completions
              mkdir -p $out/share/fish/vendor_completions.d
              mkdir -p $out/share/bash-completion/completions
              mkdir -p $out/share/zsh/site-functions

              $out/bin/bd completion fish > $out/share/fish/vendor_completions.d/bd.fish
              $out/bin/bd completion bash > $out/share/bash-completion/completions/bd
              $out/bin/bd completion zsh > $out/share/zsh/site-functions/_bd
            '';

            meta = bdBase.meta;
          };
        in
        {
          packages = {
            default = bd;

            # Keep separate completion packages for users who only want specific shells
            fish-completions = pkgs.runCommand "bd-fish-completions" { } ''
              mkdir -p $out/share/fish/vendor_completions.d
              ln -s ${bd}/share/fish/vendor_completions.d/bd.fish $out/share/fish/vendor_completions.d/bd.fish
            '';

            bash-completions = pkgs.runCommand "bd-bash-completions" { } ''
              mkdir -p $out/share/bash-completion/completions
              ln -s ${bd}/share/bash-completion/completions/bd $out/share/bash-completion/completions/bd
            '';

            zsh-completions = pkgs.runCommand "bd-zsh-completions" { } ''
              mkdir -p $out/share/zsh/site-functions
              ln -s ${bd}/share/zsh/site-functions/_bd $out/share/zsh/site-functions/_bd
            '';
          };

          apps.default = {
            type = "app";
            program = "${self.packages.${system}.default}/bin/bd";
          };

          devShells.default = pkgs.mkShell {
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
}
