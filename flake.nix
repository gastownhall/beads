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
          pkgs = nixpkgs.legacyPackages.${system};
          bd = pkgs.callPackage ./default.nix { inherit pkgs self; };
        in
        {
          packages = {
            default = bd;

            fish-completions = pkgs.runCommand "bd-fish-completions" { } ''
              mkdir -p $out/share/fish/vendor_completions.d
              ${bd}/bin/bd completion fish > $out/share/fish/vendor_completions.d/bd.fish
            '';

            bash-completions = pkgs.runCommand "bd-bash-completions" { } ''
              mkdir -p $out/share/bash-completion/completions
              ${bd}/bin/bd completion bash > $out/share/bash-completion/completions/bd
            '';

            zsh-completions = pkgs.runCommand "bd-zsh-completions" { } ''
              mkdir -p $out/share/zsh/site-functions
              ${bd}/bin/bd completion zsh > $out/share/zsh/site-functions/_bd
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
