{ pkgs, self }:
pkgs.buildGoModule {
  pname = "beads";
  version = "0.29.0";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  doCheck = false;
  # Go module dependencies hash (computed via nix build)
  vendorHash = "sha256-KRR6dXzsSw8OmEHGBEVDBOoIgfoZ2p0541T9ayjGHlI=";

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
