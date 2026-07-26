pkgs: {
  default = pkgs.beads;
  bd = pkgs.beads;

  # Separate completion packages for users who only want specific shells.
  fish-completions = pkgs.beads.passthru.fish-completions;
  bash-completions = pkgs.beads.passthru.bash-completions;
  zsh-completions = pkgs.beads.passthru.zsh-completions;
}
