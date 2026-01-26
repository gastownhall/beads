{ pkgs, self }:
pkgs.buildGoModule {
  pname = "beads";
  version = "0.49.0";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  doCheck = false;
  # Go module dependencies hash - if build fails with hash mismatch, update with the "got:" value
  vendorHash = "sha256-YU+bRLVlWtHzJ1QPzcKJ70f+ynp8lMoIeFlm+29BNPE=";

  # Git is required for tests
  nativeBuildInputs = [ pkgs.git ];

  meta = with pkgs.lib; {
    description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";
    homepage = "https://github.com/steveyegge/beads";
    license = licenses.mit;
    mainProgram = "bd";
    maintainers = [ ];
  };
}
