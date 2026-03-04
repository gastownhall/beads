{ pkgs, self, ... }:
let
  bdBase = pkgs.callPackage ./default.nix { inherit self; };

  # Wrap the base package with shell completions baked in
  bd = pkgs.stdenv.mkDerivation {
    pname = "beads";
    version = bdBase.version;

    phases = [ "installPhase" ];

    installPhase = ''
      mkdir -p $out/bin
      cp ${bdBase}/bin/bd $out/bin/bd

      # Create 'beads' alias symlink
      ln -s bd $out/bin/beads

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
  default = bd;
in
{
  inherit default bd;

  # Separate completion packages for users who only want specific shells
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
}
