{
  lib,
  self,
  buildGoModule,
  git,
  ...
}:
buildGoModule {
  pname = "beads";
  version = "0.63.0";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  doCheck = false;

  # Go module dependencies hash - if build fails with hash mismatch, update with the "got:" value
  vendorHash = "sha256-1BJsEPP5SYZFGCWHLn532IUKlzcGDg5nhrqGWylEHgY=";

  # Relax go.mod version for Nix: nixpkgs Go may lag behind the latest
  # patch release, and GOTOOLCHAIN=auto can't download in the Nix sandbox.
  postPatch = ''
    goVer="$(go env GOVERSION | sed 's/^go//')"
    go mod edit -go="$goVer"
  '';

  # Allow patch-level toolchain upgrades when a dependency's minimum Go patch
  # version is newer than nixpkgs' bundled patch version.
  env.GOTOOLCHAIN = "auto";

  # Git is required for tests
  nativeBuildInputs = [ git ];

  meta = with lib; {
    description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";
    homepage = "https://github.com/steveyegge/beads";
    license = licenses.mit;
    mainProgram = "bd";
    maintainers = [ ];
  };
}
