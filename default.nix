{ pkgs, self }:
pkgs.buildGoModule {
  pname = "beads";
  version = "0.49.5";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  doCheck = false;
  # Go module dependencies hash - if build fails with hash mismatch, update with the "got:" value
  vendorHash = "sha256-deLPoWXRsWAyehUn2QlXA/vs7zepUF3jAjUq+MFCGbI=";

  # Allow Go toolchain to auto-download newer version if needed
  # (go.mod requires 1.25.6+ but nixpkgs may have older)
  env.GOTOOLCHAIN = "auto";

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
