{buildGoModule}:
buildGoModule {
  pname = "d2-live";
  version = "0.3.0";
  src = ./.;
  vendorHash = "sha256-DxbAFVbmSGV0KAJ807WmpYTnKf1br8PbZCqvWvcn0bw=";
  doCheck = false;
  meta.mainProgram = "d2-live";
}
