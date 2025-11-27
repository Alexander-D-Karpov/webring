{ buildGoModule, lib }: buildGoModule {
  pname = "webring";
  version = "2025-11-10";

  src = ../.;
  subPackages = [ "cmd/server" ];

  vendorHash = "sha256-bwCfn3AEWKJmsy8FTkLqtx5VXIjOZ7Nux6wAogeb9JM=";

  postInstall = ''
    mv $out/bin/server $out/bin/webring-server
  '';

  meta = with lib; {
    description = "a small webring backend, written in go";
    homepage = "https://github.com/Alexander-D-Karpov/webring";
    license = licenses.unfreeRedistributable;
    mainProgram = "webring-server";
    maintainers = [ {
      name = "Damir Modyarov";
      email = "damir@otomir23.me";
      github = "otomir23";
      githubId = 21289906;
    } ];
  };
}
