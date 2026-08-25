{ buildGoModule, lib, makeWrapper, chromium }: buildGoModule {
  pname = "webring";
  version = "2025-11-10";

  src = ../.;
  subPackages = [ "cmd/server" "cmd/ringcheck" ];

  nativeBuildInputs = [ makeWrapper ];

  # MUST BE REGENERATED. Adding chromedp changed the dependency set and there was no nix
  # available to compute the new hash. The first build fails and prints the value to use.
  vendorHash = lib.fakeHash;

  postInstall = ''
    mv $out/bin/server $out/bin/webring-server

    # The checker drives a real browser, so it needs one on PATH.
    wrapProgram $out/bin/ringcheck \
      --set-default CHROME_PATH ${chromium}/bin/chromium
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
