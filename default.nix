{ pkgs, self }:
pkgs.buildGoModule {
  pname = "beads";
  version = "0.30.7";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  doCheck = false;
  # Go module dependencies hash (computed via nix build)
  vendorHash = "sha256-IsHU7IkLK22YTv2DE8lMJ2oEOc9nsFBTW36i81Z58eQ=";

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
