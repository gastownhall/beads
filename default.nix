{ pkgs, self }:
pkgs.buildGoModule {
  pname = "beads";
  version = "0.9.9";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];

  # Go module dependencies hash (computed via nix build)
  vendorHash = "sha256-9xtp1ZG7aYXatz02PDTmSRXwBDaW0kM7AMQa1RUau4U=";

  meta = with pkgs.lib; {
    description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";
    homepage = "https://github.com/steveyegge/beads";
    license = licenses.mit;
    mainProgram = "bd";
    maintainers = [ ];
  };
}
