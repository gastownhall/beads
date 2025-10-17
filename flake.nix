{
  description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachSystem [
      "x86_64-linux"
      "aarch64-linux"
      "x86_64-darwin"
      "aarch64-darwin"
    ] (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "beads";
          version = "0.9.9";

          src = self;

          # Point to the main Go package
          subPackages = [ "cmd/bd" ];

          # Go module dependencies hash (computed via nix build)
          vendorHash = "sha256-1ufUs1PvFGsSR0DTSymni3RqecEBzAm//OBUWgaTwEs=";

          meta = with pkgs.lib; {
            description = "beads (bd) - An issue tracker designed for AI-supervised coding workflows";
            homepage = "https://github.com/steveyegge/beads";
            license = licenses.mit;
            mainProgram = "bd";
            maintainers = [ ];
          };
        };

        apps.default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/bd";
        };
      }
    );
}
