{
  lib,
  stdenvNoCC,
  zig,
}:
stdenvNoCC.mkDerivation {
  pname = "spongix-symlink";
  version = "0.1.0";

  src = ./cmd/symlink;

  nativeBuildInputs = [zig];

  dontConfigure = true;

  buildPhase = ''
    runHook preBuild
    export ZIG_GLOBAL_CACHE_DIR="$TMPDIR/zig-cache"
    zig build-exe main.zig \
      -target ${
      {
        "x86_64-linux" = "x86_64-linux-musl";
        "aarch64-linux" = "aarch64-linux-musl";
        "x86_64-darwin" = "x86_64-macos";
        "aarch64-darwin" = "aarch64-macos";
      }
        .${
        stdenvNoCC.hostPlatform.system
      }
    } \
      ${lib.optionalString stdenvNoCC.hostPlatform.isDarwin "-lc"} \
      -OReleaseSmall \
      -fstrip \
      --name symlink
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    mkdir -p $out/bin
    cp symlink $out/bin/
    runHook postInstall
  '';

  # The binary is statically linked and must not reference anything at runtime.
  allowedReferences = [];

  meta = {
    description = "Minimal symlink binary for Spongix registry flakes";
    license = lib.licenses.asl20;
    platforms = ["x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin"];
  };
}
