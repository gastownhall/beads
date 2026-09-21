{
  lib,
  self,
  buildGoModule,
  git,
  ...
}:
buildGoModule {
  pname = "beads";
  version = "1.3.1-rc.1";

  src = self;

  # Point to the main Go package
  subPackages = [ "cmd/bd" ];
  tags = [ "gms_pure_go" ];
  doCheck = false;

  # proxyVendor avoids vendor/modules.txt consistency checks when the vendored
  # tree lags go.mod/go.sum.
  proxyVendor = true;
  # Recomputed for this branch: neither release/1.3.0's value nor #5931's is
  # right here, because the branch already carried an independent dependency
  # bump (d594092eb), so the #5931 cherry-pick lands on a vendor set that
  # existed on neither side. Value taken from the `got:` hash the nix-build lane
  # reported on this branch.
  vendorHash = "sha256-DQdauEx5g48Xbxrz4wGLx4vkrQoYQX3FUx+7N/Y6YV4=";

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
