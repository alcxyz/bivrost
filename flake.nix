{
  description = "Bivrost: local sessions for Azure Bastion and SSH development environments";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      sourceVersion = nixpkgs.lib.removeSuffix "\n" (builtins.readFile ./VERSION);
      # The source revision tells builds of the same VERSION apart, for
      # example a release and a later dev commit.
      revision = self.shortRev or self.dirtyShortRev or "unknown";
    in
    {
      packages = forAllSystems (pkgs: rec {
        bivrost = pkgs.callPackage ./nix/package.nix {
          src = self;
          version = "${sourceVersion}+${revision}";
        };
        default = bivrost;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            goreleaser
          ];
        };
      });
    };
}
