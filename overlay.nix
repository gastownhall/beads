# Curried on the flake's `self` so default.nix's `src = self;` keeps working
# outside the `pkgs.callPackage` auto-argument machinery (callPackage cannot
# supply a flake output on its own). flake.nix applies this as
# `import ./overlay.nix self`.
self: final: prev: {
  # Unwrapped Go build of the bd binary. Exposed so downstream consumers can
  # override inputs (vendorHash, buildGoModule, etc.) via the standard
  # callPackage `.override` pattern, e.g.
  #
  #   final: prev: {
  #     beads-unwrapped = prev.beads-unwrapped.override (prevArgs: {
  #       buildGoModule = args:
  #         prevArgs.buildGoModule (args // { vendorHash = "..."; });
  #     });
  #   }
  beads-unwrapped = final.callPackage ./default.nix {
    inherit self;
    buildGoModule = final.buildGo126Module;
  };

  # Wrap the unwrapped binary with shell completions and a `beads` alias.
  beads = final.stdenv.mkDerivation {
    pname = "beads";
    version = final.beads-unwrapped.version;

    phases = [ "installPhase" ];

    env.BD_DISABLE_METRICS = "1";
    env.BD_DISABLE_EVENT_FLUSH = "1";

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
      # Per-shell completion outputs for users who only want one shell.
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
