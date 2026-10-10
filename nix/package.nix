# Bivrost with the command-line tools its sessions run. Deployments supply
# their environment catalogue as configuration (BIVROST_CATALOGUE_FILE or
# catalogue.json in the user's bivrost configuration directory), not in this
# package.
{
  lib,
  buildGoModule,
  makeWrapper,
  azure-cli,
  openssh,
  kubectl,
  kubelogin,
  src,
  version,
}:
let
  azureCli = azure-cli.withExtensions (
    with azure-cli.extensions;
    [
      bastion
      ssh
    ]
  );
in
buildGoModule {
  pname = "bivrost";
  inherit src version;
  # go.mod has no external modules.
  vendorHash = null;
  subPackages = [ "cmd/bivrost" ];
  env.CGO_ENABLED = 0;
  nativeBuildInputs = [ makeWrapper ];
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];
  postInstall = ''
    wrapProgram "$out/bin/bivrost" \
      --prefix PATH : ${
        lib.makeBinPath [
          azureCli
          openssh
          kubectl
          kubelogin
        ]
      }
  '';
  meta = {
    description = "Local sessions for Azure Bastion and SSH development environments";
    homepage = "https://github.com/alcxyz/bivrost";
    license = lib.licenses.mit;
    mainProgram = "bivrost";
    platforms = lib.platforms.unix;
  };
}
