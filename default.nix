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
  # TODO(1.3.1-rc.2): recompute this hash before merge. Neither the release
  # branch's own value nor #5931's is correct here: the branch already carried
  # an independent dependency bump (d594092eb), so this cherry-pick lands on a
  # vendor set that has existed on neither side. The value below is #5931's and
  # is known-stale; it is a placeholder. Run `nix build .#default` on this
  # branch and substitute the `got:` hash. (No nix on the machine that made this
  # commit, and a guessed hash fails the build loudly, so it is left explicit
  # rather than fabricated.)
  vendorHash = "sha256-wDU1tw/4t2CetwQl4+zUhlDI70dSasLLI2Gy66ZyhQQ=";

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
