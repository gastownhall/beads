{
  lib,
  self,
  buildGoModule,
  git,
  ...
}:
buildGoModule {
  pname = "beads";
  version = "1.0.3";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  tags = [ "gms_pure_go" ];
  doCheck = false;

  # proxyVendor avoids vendor/modules.txt consistency checks when the vendored
  # tree lags go.mod/go.sum.
  proxyVendor = true;
  vendorHash = "sha256-S/NavjGH6VSPU+rCtqtviOcGhgXc6VZUXCUhasSdUGU=";

  # Match go.mod to the selected Nix Go toolchain. buildGoModule also builds
  # vendored dependencies in the Nix sandbox, where toolchain downloads are not
  # available.
  postPatch = ''
    goVer="$(go env GOVERSION | sed 's/^go//')"
    go mod edit -go="$goVer"
  '';

  env.GOTOOLCHAIN = "local";

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
