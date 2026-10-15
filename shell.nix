{ pkgs ? import (builtins.fetchTarball {
    url = "https://github.com/NixOS/nixpkgs/archive/335f0738cb2fa9708f3f428e39d2eae975d1338d.tar.gz";
    sha256 = "sha256-THPEF2po0fsoH8gNtp+Ae0XFDJH3N/ol7xO3v6VMTJU=";
  }) {} }:

pkgs.mkShell {
  packages = with pkgs; [
    curl
    docker-client
    docker-compose
    dotnet-sdk_10
    ffmpeg
    go_1_25
    gofumpt
    golangci-lint
    (mitmproxy.overridePythonAttrs (_: {
      # nixpkgs currently carries msgpack 1.2 with mitmproxy's stale <=1.1.2 metadata.
      dontCheckRuntimeDeps = true;
    }))
    python3
    pkg-config
    sqlite
    vips
  ];

  DOTNET_CLI_TELEMETRY_OPTOUT = "1";
  DOTNET_NOLOGO = "1";
  PYTHONDONTWRITEBYTECODE = "1";
}
