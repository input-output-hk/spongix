{
  pkgs,
  spongix,
}: let
  # Test signing keys (from repo root)
  secretKey = "local:I4J3C7y7Xq3ir7P8fzd/3nAOJbmk20kHrSsQZB/wvu2h+RWYFLI/4MLmj6gOetZ7Y8LFCnfgTg4scfZZ1FoUrA==";
  publicKey = "local:ofkVmBSyP+DC5o+oDnrWe2PCxQp34E4OLHH2WdRaFKw=";

  cacheUrl = "http://localhost:8888/public";
  # Spongix stores/serves NARs uncompressed and rejects compressed uploads.
  # Newer Nix compresses `nix copy --to` uploads (.nar.xz) by default, so push
  # URLs must request compression=none (matches the spongixctl push flow).
  pushUrl = "${cacheUrl}?compression=none";

  spongixConfig = pkgs.writeText "spongix-config.json" (builtins.toJSON {
    listen = "0.0.0.0:8888";
    log_level = "debug";
    log_mode = "development";
    chunks = {
      average_size = 65536;
      store_type = "pebble";
      pebble_dir = "/var/lib/spongix/db";
    };
    namespaces.public = {
      cache_info_priority = 50;
      store_type = "pebble";
      substituters = [];
    };
    # "proxy" namespace proxies to nix-serve on port 9999.
    # Cannot proxy to "public" because Pebble's shared index store
    # deduplicates across namespaces (content is immediately visible).
    namespaces.proxy = {
      cache_info_priority = 50;
      store_type = "pebble";
      substituters = ["http://localhost:9999"];
    };
  });

  postBuildHook = pkgs.writeShellScript "post-build-hook" ''
    set -eu
    export IFS=' '
    echo "post-build-hook: uploading $OUT_PATHS" >&2
    exec nix copy --to "${pushUrl}" $OUT_PATHS
  '';

  system = pkgs.stdenv.hostPlatform.system;
in
  pkgs.testers.nixosTest {
    name = "spongix-e2e";

    nodes.machine = {lib, ...}: {
      virtualisation.memorySize = 2048;

      # Spongix service with Pebble backend
      systemd.services.spongix = {
        description = "Spongix binary cache proxy";
        after = ["network.target"];
        wantedBy = ["multi-user.target"];
        serviceConfig = {
          ExecStart = "${spongix}/bin/spongix --config ${spongixConfig}";
          StateDirectory = "spongix";
          Restart = "on-failure";
          RestartSec = 1;
        };
      };

      # nix-serve as an upstream binary cache (serves the local nix store)
      services.nix-serve = {
        enable = true;
        port = 9999;
        secretKeyFile = "/etc/nix/signing-key";
      };

      # Signing key
      environment.etc."nix/signing-key" = {
        text = secretKey;
        mode = "0600";
      };

      # Configure Nix daemon to use spongix and sign builds
      nix.settings = {
        substituters = lib.mkForce [cacheUrl];
        trusted-substituters = [cacheUrl];
        trusted-public-keys = [publicKey];
        secret-key-files = "/etc/nix/signing-key";
        # Don't cache narinfo results so re-fetches always hit the server
        narinfo-cache-negative-ttl = 0;
        narinfo-cache-positive-ttl = 0;
        experimental-features = ["nix-command" "flakes"];
        post-build-hook = postBuildHook;
      };

      environment.systemPackages = [pkgs.curl];
    };

    testScript = ''
      machine.wait_for_unit("multi-user.target")
      machine.wait_for_unit("spongix")
      machine.wait_for_unit("nix-serve")

      # Wait for spongix HTTP to be ready
      machine.wait_until_succeeds("curl -sf http://localhost:8888/public/nix-cache-info")

      # Sanity check: cache info endpoint
      result = machine.succeed("curl -sf http://localhost:8888/public/nix-cache-info")
      assert "StoreDir: /nix/store" in result, f"Bad cache info: {result}"
      machine.log("cache info OK")


      # ── Test 1: nix copy --to / --from ────────────────────────────
      machine.log("=== Test 1: nix copy --to / --from ===")

      # Build a fresh derivation inside the VM
      out1 = machine.succeed(
          'nix-build --no-out-link -E '
          "'derivation { "
          '  name = "e2e-copy-test"; '
          '  system = "${system}"; '
          '  builder = "/bin/sh"; '
          '  args = ["-c" "echo copy-test-content > $out"]; '
          "}'"
      ).strip()
      machine.log(f"Built: {out1}")

      # Push to spongix
      machine.succeed(f'nix copy --to "${pushUrl}" {out1}')
      machine.log("nix copy --to succeeded")

      # Verify narinfo is in the cache
      hash1 = out1.split("/")[-1].split("-")[0]
      narinfo1 = machine.succeed(f"curl -sf http://localhost:8888/public/{hash1}.narinfo")
      assert "StorePath:" in narinfo1, f"Bad narinfo: {narinfo1}"
      assert "Sig:" in narinfo1, f"Missing signature in narinfo: {narinfo1}"
      machine.log("narinfo OK, signed")

      # Delete from local store
      machine.succeed(f"nix-store --delete {out1}")
      machine.fail(f"test -e {out1}")
      machine.log("deleted from local store")

      # Fetch back from cache
      machine.succeed(f"nix copy --from http://localhost:8888/public {out1}")
      machine.succeed(f"test -e {out1}")
      content1 = machine.succeed(f"cat {out1}").strip()
      assert content1 == "copy-test-content", f"Wrong content: {content1}"
      machine.log("nix copy --from succeeded, content verified")


      # ── Test 2: post-build-hook ───────────────────────────────────
      machine.log("=== Test 2: post-build-hook ===")

      # Build a derivation — the hook should automatically copy it to spongix
      out2 = machine.succeed(
          'nix-build --no-out-link -E '
          "'derivation { "
          '  name = "e2e-hook-test"; '
          '  system = "${system}"; '
          '  builder = "/bin/sh"; '
          '  args = ["-c" "echo hook-test-content > $out"]; '
          "}'"
      ).strip()
      machine.log(f"Built with hook: {out2}")

      # The post-build-hook runs synchronously before nix-build returns,
      # so by now it should already be in the cache.
      hash2 = out2.split("/")[-1].split("-")[0]
      machine.succeed(f"curl -sf http://localhost:8888/public/{hash2}.narinfo")
      machine.log("post-build-hook uploaded narinfo")

      # Delete locally and fetch from cache
      machine.succeed(f"nix-store --delete {out2}")
      machine.fail(f"test -e {out2}")
      machine.succeed(f"nix copy --from http://localhost:8888/public {out2}")
      machine.succeed(f"test -e {out2}")
      content2 = machine.succeed(f"cat {out2}").strip()
      assert content2 == "hook-test-content", f"Wrong content: {content2}"
      machine.log("post-build-hook content verified")


      # ── Test 3: substituter fetch (nix-store --realise) ───────────
      machine.log("=== Test 3: substituter fetch via nix-store -r ===")

      # Build another derivation and push to cache
      out3 = machine.succeed(
          'nix-build --no-out-link -E '
          "'derivation { "
          '  name = "e2e-subst-test"; '
          '  system = "${system}"; '
          '  builder = "/bin/sh"; '
          '  args = ["-c" "echo subst-test-content > $out"]; '
          "}'"
      ).strip()
      machine.log(f"Built: {out3}")

      # This was built with the post-build-hook active, so it's already in the cache.
      # Delete locally.
      machine.succeed(f"nix-store --delete {out3}")
      machine.fail(f"test -e {out3}")

      # Realise via substituter — nix should fetch from spongix
      machine.succeed(f"nix-store --realise {out3}")
      machine.succeed(f"test -e {out3}")
      content3 = machine.succeed(f"cat {out3}").strip()
      assert content3 == "subst-test-content", f"Wrong content: {content3}"
      machine.log("substituter fetch verified")


      # ── Test 4: Upstream proxy caching ─────────────────────────
      machine.log("=== Test 4: upstream proxy caching ===")

      # The "proxy" namespace proxies to nix-serve on port 9999, which serves
      # the local nix store. We add a path to the store (without building, so
      # the post-build-hook doesn't fire and the path stays OUT of spongix's
      # Pebble DB). nix-serve serves it, spongix proxies to it.
      #
      # Flow:
      #   1st request → spongix has no index → HEAD nix-serve → proxies the
      #                 narinfo (200, X-Cache: MISS) + background cache job
      #   later        → spongix has the index → 200 direct serve (X-Cache: HIT)

      # Add a file to the nix store without building (no post-build-hook)
      machine.succeed("echo proxy-test-content > /tmp/proxy-test-file")
      out4 = machine.succeed("nix-store --add /tmp/proxy-test-file").strip()
      machine.log(f"Added to store (no hook): {out4}")
      hash4 = out4.split("/")[-1].split("-")[0]

      # Verify nix-serve can serve it
      machine.wait_until_succeeds(f"curl -sf http://localhost:9999/{hash4}.narinfo")
      machine.log("nix-serve serves the narinfo")

      # Verify proxy namespace is up
      machine.succeed("curl -sf http://localhost:8888/proxy/nix-cache-info")

      # First request: content is NOT in Pebble, only in nix-serve. Spongix
      # HEADs nix-serve, finds it, and proxies the narinfo (200, X-Cache: MISS),
      # rewriting the compression fields, and kicks off a background cache job.
      # Narinfos are proxied (not redirected) so spongix can rewrite Compression
      # for uncompressed storage before the client sees it.
      first = machine.succeed(
          f"curl -s -o /dev/null -D - -w 'code=%{{http_code}}' "
          f"http://localhost:8888/proxy/{hash4}.narinfo"
      )
      assert "code=200" in first, f"Expected 200 on first proxy request: {first!r}"
      assert "X-Cache: MISS" in first, f"Expected X-Cache: MISS on first proxy request: {first!r}"
      machine.log("first proxy request: 200, X-Cache: MISS")

      # Wait for the background cache job to store the path in Pebble, after
      # which requests are served directly from cache (X-Cache: HIT).
      machine.wait_until_succeeds(
          f"curl -s -o /dev/null -D - http://localhost:8888/proxy/{hash4}.narinfo | grep -qi '^X-Cache: HIT'"
      )
      machine.log("background cache populated (X-Cache: HIT)")

      # Cache HIT: served from Pebble and signed by spongix.
      narinfo_cached = machine.succeed(f"curl -sf http://localhost:8888/proxy/{hash4}.narinfo")
      assert "StorePath:" in narinfo_cached, f"Bad cached narinfo: {narinfo_cached}"
      assert "Sig:" in narinfo_cached, f"Missing signature in cached narinfo: {narinfo_cached}"
      machine.log("proxy cached narinfo verified (signed)")

      # Note: NAR fetch through proxy is not tested here because nix-serve
      # generates NAR URLs with a different format ({storehash}-{narhash}.nar)
      # than spongix's route pattern expects ({52-char-hash}.nar[.ext]).
      # In production, upstream substituters use the standard format.


      machine.log("=== All e2e tests passed! ===")
    '';
  }
