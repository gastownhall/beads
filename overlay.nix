final: prev: {
  beads-unwrapped = final.callPackage ./default.nix { };

  beads = final.stdenv.mkDerivation {
    pname = "beads";
    version = final.beads-unwrapped.version;

    phases = [ "installPhase" ];

    installPhase = ''
      mkdir -p $out/bin
      cp ${final.beads-unwrapped}/bin/bd $out/bin/bd

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

    meta = final.beads-unwrapped.meta;

    passthru = {
      fish-completions = final.runCommand "bd-fish-completions" { } ''
        mkdir -p $out/share/fish/vendor_completions.d
        ln -s ${final.beads}/share/fish/vendor_completions.d/bd.fish $out/share/fish/vendor_completions.d/bd.fish
      '';

      bash-completions = final.runCommand "bd-bash-completions" { } ''
        mkdir -p $out/share/bash-completion/completions
        ln -s ${final.beads}/share/bash-completion/completions/bd $out/share/bash-completion/completions/bd
      '';

      zsh-completions = final.runCommand "bd-zsh-completions" { } ''
        mkdir -p $out/share/zsh/site-functions
        ln -s ${final.beads}/share/zsh/site-functions/_bd $out/share/zsh/site-functions/_bd
      '';
    };
  };
}
