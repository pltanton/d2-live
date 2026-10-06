{buildGoModule}:
buildGoModule {
  pname = "d2-live";
  version = "0.3.0";
  src = ./.;
  vendorHash = "sha256-ujQhLKtMwKBY+ZHFPpjX9d5HclKgQs95W0CGZJnQjxA=";
  doCheck = false;
  meta.mainProgram = "d2-live";
}
