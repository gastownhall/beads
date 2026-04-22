pkgs: {
  default = pkgs.beads;
  bd = pkgs.beads;
  fish-completions = pkgs.beads.passthru.fish-completions;
  bash-completions = pkgs.beads.passthru.bash-completions;
  zsh-completions = pkgs.beads.passthru.zsh-completions;
}
