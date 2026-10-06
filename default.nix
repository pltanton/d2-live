{buildGoModule}:
buildGoModule {
  pname = "d2-live";
  version = "0.4.0";
  src = ./.;
  vendorHash = "sha256-vUZxAk8yBqmBQFh8n7r+vP/18U/e2g7VGO47lGlYtCM=";
  doCheck = false;
  meta.mainProgram = "d2-live";
}
