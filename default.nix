{
  lib,
  self,
  buildGoModule,
  git,
  icu,
  ...
}:
buildGoModule {
  pname = "beads";
  version = "1.0.2";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  doCheck = false;

  # gms_pure_go uses Go's stdlib regex instead of go-icu-regex
  tags = [ "gms_pure_go" ];

  # proxyVendor avoids the vendor/modules.txt consistency check that fails
  # due to an upstream go.mod/vendor sync issue (needs go mod tidy upstream).
  # The hash below is the go modules hash used to build the local proxy.
  proxyVendor = true;
  vendorHash = "sha256-2nQkAIxhAUVNC6SPwocjlfbgt6oG8WFapw/V+j2Pang=";

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
    homepage = "https://github.com/gastownhall/beads";
    license = licenses.mit;
    mainProgram = "bd";
    maintainers = [ ];
  };
}
