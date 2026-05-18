{buildGoModule}:
buildGoModule {
  pname = "d2-live";
  version = "0.1.0";
  src = ./.;
  vendorHash = "sha256-QmKGsS3lAr6Q2IvD4memxSP6NerJTrgbK0nMpBC6mzk=";
  doCheck = false;
  meta.mainProgram = "d2-live";
}
