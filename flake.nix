{
  description = "juggler: headless agent substrate and llama-server control plane (extracted from clown, clown#258)";

  inputs = {
    # conformist provides the linter/formatter multiplexer, its Nix module
    # library (conformist.lib), and the eng-convention presets. Following its
    # nixpkgs-master/utils pins keeps this flake's closure shared with conformist's.
    conformist.url = "https://code.linenisgreat.com/conformist/archive/master.tar.gz";
    conformist.inputs.igloo.follows = "igloo";
    nixpkgs.follows = "conformist/nixpkgs-master";
    utils.follows = "conformist/utils";

    # igloo carries the Go build helpers: godyn (buildGoAuto, buildGodynModule,
    # the go.nix manifest, godyn-go / godyn-test) and mkGoPkgs. See godyn(7).
    igloo.url = "https://code.linenisgreat.com/igloo/archive/master.tar.gz";
    igloo.inputs.nixpkgs-master.follows = "nixpkgs";

    # just-us is the eng `just` fork. It supplies BOTH the devShell's `just` and
    # the justfile-orphan-summary linter module: that check reads the fork-only
    # `doc_prelude` field of `just --dump --dump-format json`, which upstream
    # `just` never emits, so running it against `pkgs.just` would pass
    # vacuously. The coupling lives in just-us rather than conformist because
    # conformist must stay strictly upstream of its consumers. Its nixpkgs /
    # flake-utils / conformist inputs follow this flake's so the closure stays
    # shared; its `bats` input deliberately keeps its own pin (see just-us's
    # flake.nix) and is unused here.
    just-us.url = "git+https://code.linenisgreat.com/just-us.git";
    just-us.inputs.nixpkgs.follows = "nixpkgs";
    just-us.inputs.flake-utils.follows = "utils";
    just-us.inputs.conformist.follows = "conformist";

    # amarbel-llc/bats: the batsLane build helper and the bats-libs bundle
    # (bats-support, bats-assert, bats-emo, bats-island).
    bats.url = "https://code.linenisgreat.com/bats/archive/master.tar.gz";
    bats.inputs.igloo.follows = "igloo";
    bats.inputs.nixpkgs-master.follows = "nixpkgs";
    bats.inputs.utils.follows = "utils";
    bats.inputs.conformist.follows = "conformist";

    # ringmaster: the job platform. Consumed two ways: as a Go module through
    # go.nix's flakeInputs bridge (pkgs/jobwake; juggler exact-matches its
    # protocol version), and as the `ringmaster` binary whose store path is
    # burned into juggler (main.RingmasterPath).
    ringmaster.url = "https://code.linenisgreat.com/ringmaster/archive/master.tar.gz";
    ringmaster.inputs.igloo.follows = "igloo";
    ringmaster.inputs.nixpkgs-master.follows = "nixpkgs";
    ringmaster.inputs.utils.follows = "utils";
    ringmaster.inputs.bats.follows = "bats";
    ringmaster.inputs.conformist.follows = "conformist";

    # tommy: CST-preserving TOML library (go.nix flakeInputs bridge); the remote
    # models registry is written through it so hand-written comments survive.
    tommy.url = "https://code.linenisgreat.com/tommy/archive/master.tar.gz";
    tommy.inputs.igloo.follows = "igloo";
    tommy.inputs.nixpkgs-master.follows = "nixpkgs";
    tommy.inputs.utils.follows = "utils";
    tommy.inputs.bats.follows = "bats";
    tommy.inputs.conformist.follows = "conformist";

    # troupe: BINARY-only (main.TroupePath); juggler does not import its Go.
    troupe.url = "https://code.linenisgreat.com/troupe/archive/master.tar.gz";
    troupe.inputs.nixpkgs.follows = "igloo";
    troupe.inputs.utils.follows = "utils";
    troupe.inputs.treefmt-nix.follows = "igloo/treefmt-nix";
    troupe.inputs.ringmaster.follows = "ringmaster";
    troupe.inputs.nixpkgs-master.follows = "nixpkgs";

    # llama-cpp with Anthropic Messages API (/v1/messages) support — requires
    # PR #17570 (merged 2025-11-28). Build 6981 in nixos-25.11 predates it.
    # Only juggler uses this pin (main.LlamaServerPath).
    nixpkgs-llama.url = "github:NixOS/nixpkgs/c0df0d088ab33122b402ea31cb5f7e1df7536036";

    # NOTE: moxy is deliberately NOT an input. moxy's flake pins clown, and
    # clown will pin juggler, so taking moxy here would make the repos pin each
    # other. main.MoxyPath is burned in by the consumer instead (lib.mkJugglerGo).
  };

  outputs =
    inputs@{
      self,
      conformist,
      igloo,
      just-us,
      bats,
      ringmaster,
      troupe,
      nixpkgs-llama,
      utils,
      ...
    }:
    (utils.lib.eachDefaultSystem (
      system:
      let
        # igloo's pkgs carry everything plain nixpkgs does, plus the godyn
        # overlay (buildGoAuto, buildGodynModule, mkGoPkgs, godyn-go, godyn-test).
        pkgs = import igloo { inherit system; };
        lib = pkgs.lib;
        pkgs-llama = import nixpkgs-llama { inherit system; };

        # eng-versioning(7): version.env is the single source of truth. Matching
        # on the `_VERSION=` suffix keeps this line independent of the repo name.
        jugglerVersion = builtins.head (
          builtins.match ".*_VERSION=([^\n]+).*" (builtins.readFile ./version.env)
        );

        # Whole-tree date harvested from flake metadata, rendered in mdoc's
        # "Month D, YYYY" convention and substituted for @MDOCDATE@ in the man
        # pages at build time.
        flakeDate = self.lastModifiedDate or "19700101000000";
        monthNames = {
          "01" = "January";
          "02" = "February";
          "03" = "March";
          "04" = "April";
          "05" = "May";
          "06" = "June";
          "07" = "July";
          "08" = "August";
          "09" = "September";
          "10" = "October";
          "11" = "November";
          "12" = "December";
        };
        mdocDate = "${monthNames.${builtins.substring 4 2 flakeDate}} ${
          toString (lib.toIntBase10 (builtins.substring 6 2 flakeDate))
        }, ${builtins.substring 0 4 flakeDate}";

        # The Go source: cmd/ (the binary and its two test-fixture mains),
        # internal/ (the juggler* packages) and pkgs/ (the public RPC facade).
        goSrc = lib.fileset.toSource {
          root = ./.;
          fileset = lib.fileset.unions [
            ./cmd
            ./internal
            ./pkgs
          ];
        };

        # Shared shape of every Go build here: goSrc plus the go.nix manifest
        # (igloo FDR 0008), the only source of truth for the module's
        # dependencies. go.mod and gomod2nix.toml are rendered from it inside
        # nix, flakeInputs (ringmaster, tommy) resolve to
        # `inputs.<name>.packages.<system>.go-pkgs` (igloo RFC-0001), and the
        # godyn package graph is derived from the render. `cc` keeps godyn's
        # graph CGO_ENABLED=1 so its binaries match bga's.
        buildJugglerGo =
          {
            nativeArgs ? { },
            ...
          }@args:
          pkgs.buildGoAuto (
            builtins.removeAttrs args [ "nativeArgs" ]
            // {
              src = goSrc;
              manifest = ./go.nix;
              inherit inputs;
              nativeArgs = {
                cc = pkgs.stdenv.cc;
              }
              // nativeArgs;
            }
          );

        ringmasterPkg = ringmaster.packages.${system}.default;
        troupePkg = troupe.packages.${system}.troupe;
        llamaServerPath = "${pkgs-llama.llama-cpp}/bin/llama-server";

        # mkJugglerGo { moxyPath } builds the juggler binary. The agent-substrate
        # verbs (FDR 0019) consume ringmaster, troupe, systemd-run and
        # systemctl as binaries: their store paths are burned in as the
        # defaults behind --ringmaster/--troupe/--systemd-run/--systemctl and
        # JUGGLER_*_BIN. moxy is NOT an input of this flake, so MoxyPath is
        # empty by default and `juggler run` resolves moxy from PATH (or
        # --moxy / JUGGLER_MOXY_BIN); a consumer passes its own moxy here to
        # burn it in (clown's mkJuggler does).
        mkJugglerGo =
          {
            moxyPath ? "",
          }:
          buildJugglerGo {
            pname = "juggler";
            version = jugglerVersion;
            subPackages = [ "cmd/juggler" ];
            ldflags = [
              "-s"
              "-w"
            ];
            ldflagsX = {
              "main.LlamaServerPath" = llamaServerPath;
              "main.RingmasterPath" = "${ringmasterPkg}/bin/ringmaster";
              "main.TroupePath" = "${troupePkg}/bin/troupe";
              # pkgs.systemd is linux-only; an empty path falls through to the
              # bare name (resolveBin) where there is no systemd (darwin).
              "main.SystemdRunPath" =
                lib.optionalString pkgs.stdenv.hostPlatform.isLinux "${pkgs.systemd}/bin/systemd-run";
              "main.SystemctlPath" =
                lib.optionalString pkgs.stdenv.hostPlatform.isLinux "${pkgs.systemd}/bin/systemctl";
            }
            // lib.optionalAttrs (moxyPath != "") { "main.MoxyPath" = moxyPath; };
          };

        # PATH-resolved-moxy default: packages.juggler, the hm module, tests.
        juggler-go = mkJugglerGo { };

        # fake-llama-server: a stand-in for llama-server used by the bats e2e
        # lane. Real llama-cpp is too heavy and slow to spin up under bats. It
        # serves /health (200 OK) and /v1/models, which is all the launcher
        # waits on.
        fake-llama-server-go = buildJugglerGo {
          pname = "fake-llama-server";
          version = jugglerVersion;
          subPackages = [ "cmd/juggler/testdata/fake-llama-server" ];
          ldflags = [
            "-s"
            "-w"
          ];
        };

        # fake-platform: the juggler_lifecycle.bats lane's stand-in for the
        # agent substrate's platform binaries (a stateful fake ringmaster,
        # troupe and systemd-run, selected by JUGGLERRUNTEST_FAKE, plus a fake
        # OpenRouter Decisions endpoint). Reuses the Go tests' fakes in
        # internal/jugglerrun/jugglerruntest.
        fake-platform-go = buildJugglerGo {
          pname = "fake-platform";
          version = jugglerVersion;
          subPackages = [ "cmd/juggler/testdata/fake-platform" ];
          ldflags = [
            "-s"
            "-w"
          ];
        };

        # godyn's per-package go test lane (godyn systems only): each tested
        # package's test binary is built and run in its own CA derivation.
        # testEnv hands the launcher tests a prebuilt llama-server stand-in
        # (a per-package test run has no `go` to build it).
        jugglerGodynTests = pkgs.buildGodynModule {
          pname = "juggler";
          version = jugglerVersion;
          src = goSrc;
          manifest = ./go.nix;
          inherit inputs;
          cc = pkgs.stdenv.cc;
          tests = true;
          testEnv = {
            CLOWN_TEST_FAKE_LLAMA_SERVER = "${fake-llama-server-go}/bin/fake-llama-server";
          };
        };

        # Exposed only where buildGoAuto's default picks godyn (igloo's
        # godynSystems); elsewhere the check falls back to building juggler.
        goTests =
          if juggler-go.passthru.backend == "native" then jugglerGodynTests.passthru.checkAll else juggler-go;

        # Public Go packages for other fleet modules (igloo RFC-0001 go-pkgs
        # producer convention): pkgs/jugglerrpc, the daemon wire facade clown
        # links for model resolution.
        inherit
          (pkgs.mkGoPkgs {
            src = self;
            manifest = ./go.nix;
            inherit inputs;
          })
          go-pkgs
          go-pkgs-test
          ;

        # The juggler-authored man pages (paths relative to share/man/); the
        # derivation substitutes @MDOCDATE@ into exactly this set.
        authoredManPages = [
          "man1/juggler.1"
          "man7/juggler.7"
          "man7/juggler-testing.7"
        ];
        juggler-manpages =
          pkgs.runCommand "juggler-manpages"
            {
              inherit mdocDate;
              authoredPages = lib.concatStringsSep " " authoredManPages;
            }
            ''
              for section in 1 7; do
                mkdir -p $out/share/man/man$section
              done
              cp ${./man/man1}/*.1 $out/share/man/man1/
              cp ${./man/man7}/*.7 $out/share/man/man7/
              chmod -R u+w $out/share/man
              for rel in $authoredPages; do
                  page="$out/share/man/$rel"
                  sed -i "s/@MDOCDATE@/$mdocDate/g" "$page"
                  if grep -q '@MDOCDATE@' "$page"; then
                      echo "juggler-manpages: @MDOCDATE@ left unsubstituted in $page" >&2
                      exit 1
                  fi
              done
            '';

        # The binary plus its man pages: what a consumer installs.
        juggler-with-manpages = pkgs.symlinkJoin {
          name = "juggler-${jugglerVersion}";
          paths = [
            juggler-go
            juggler-manpages
          ];
        };

        batsLibs = bats.packages.${system}.bats-libs;
        batsBinaries = {
          # `juggler daemon` (the llama-server control plane) against a Go
          # stand-in for llama-server: juggler.bats.
          JUGGLER_BIN = {
            base = juggler-go;
            name = "juggler";
          };
          FAKE_LLAMA_SERVER_BIN = {
            base = fake-llama-server-go;
            name = "fake-llama-server";
          };
          # The agent-substrate verbs (FDR 0019) against fake
          # ringmaster/troupe/systemd-run and a fake Decisions endpoint:
          # juggler_lifecycle.bats.
          FAKE_PLATFORM_BIN = {
            base = fake-platform-go;
            name = "fake-platform";
          };
          # The REAL ringmaster, driven directly by juggler_reaper.bats
          # (`ringmaster reap`, clown#257).
          RINGMASTER_BIN = {
            base = ringmasterPkg;
            name = "ringmaster";
          };
        };
        batsLaneOutputs = import ./bats.nix {
          inherit pkgs batsBinaries;
          batsLane = bats.lib.${system}.batsLane;
          bats-libs = batsLibs;
        };

        # `nix fmt` entry point. Repo overlay lives in ./conformist.nix.
        # `.default` is the bga (buildGoApplication) build on every system —
        # platform-agnostic, no ca-derivations needed.
        conformistPkg = conformist.packages.${system}.default;

        # The fork's `just`: the devShell's command runner AND the binary EVERY
        # justfile-* linter invokes. Never substitute `pkgs.just` — those checks
        # read fork-only parser data, so a stock `just` either rejects the format
        # outright or reads as zero findings.
        justPkg = just-us.packages.${system}.default;

        # Pure lane: the eng preset + the just-us justfile roster + this repo's
        # own formatters/excludes (./conformist.nix). Drives `nix fmt` and the
        # sandboxed `checks.formatting`.
        eval = conformist.lib.evalModule pkgs {
          imports = [
            conformist.lib.presets.eng
            # The justfile-convention linters ship from just-us, NOT conformist.
            # They read `just --dump --dump-format model` (and, for the
            # orphaned-summary rule, `doc_prelude`) — fork-only parser data — so
            # the coupling lives in the repo that owns the parser, and conformist
            # stays strictly upstream of it. conformist-justfile(7) remains the
            # normative prose home for the rules themselves.
            just-us.lib.conformistPresets.justfile
            ./conformist.nix
          ];
          package = conformistPkg;

          # ONE setting for the whole justfile-linter family: every rule in the
          # roster reads it. It MUST be the fork.
          linters.justfile-common.justPackage = justPkg;
        };

        # Impure lane: the git-state checks (git-remotes, sweatfile, agents-md)
        # plus this repo's impure overlay (./conformist-impure.nix, where
        # `conformist conform --allow-host` persists the git-remotes allowlist).
        # They need a live .git / host tools, so they run via `just lint-worktree`
        # against the working tree, not the sandboxed check.
        impureEval = conformist.lib.evalModule pkgs {
          imports = [
            conformist.lib.presets.eng-impure
            ./conformist-impure.nix
          ];
          package = conformistPkg;
          projectRootFile = "flake.nix";
        };
      in
      {
        packages = {
          default = juggler-with-manpages;
          juggler = juggler-go;
          manpages = juggler-manpages;
          inherit
            go-pkgs
            go-pkgs-test
            fake-llama-server-go
            fake-platform-go
            ;
          bats-libs = batsLibs;

          # A git pre-commit hook running THIS repo's config (conformist#47/#51):
          # `conformist --staged --exit-zero-on-fix` wrapped with the generated
          # store-path config, so it formats staged files with the SAME pinned
          # toolchain as `nix fmt` — no reliance on the formatters being on the
          # author's PATH (the silent-skip trap of a bare `conformist --staged`).
          # It is on the devShell PATH as `conformist-pre-commit`; wire the
          # sweatfile hook to that name (see ./sweatfile).
          conformist-pre-commit = eval.config.build.preCommit;

          # The merge-time repair sibling (conformist#54): `conformist --commit
          # --amend --exit-zero-on-fix`, same store-pinned toolchain, on the
          # devShell PATH as `conformist-repair`. It is the supported command for a
          # spinclass pre-merge REPAIR hook. Exposed and available here; the
          # sweatfile leaves `repair = "conformist-repair"` as a documented opt-in,
          # since with the per-commit hook active the tree is already conformant at
          # merge time (see ./sweatfile).
          conformist-repair = eval.config.build.repair;
          conformist-impure-config = impureEval.config.build.configFile;
        }
        // batsLaneOutputs;

        # What a consumer builds on top of: lib.<system>.mkJugglerGo { moxyPath }
        # gives a juggler with the consumer's moxy burned in (clown#258 plan,
        # §1.5), without this flake taking moxy as an input.
        lib = {
          inherit mkJugglerGo;
        };

        formatter = eval.config.build.wrapper;

        checks = {
          formatting = eval.config.build.check self;
          tests = goTests;
          bats-default = batsLaneOutputs.bats-default;
          # This module is a standalone repo now (clown#258): nothing in it may
          # import a clown package, or the two repos would be cyclic in Go.
          juggler-boundary = pkgs.runCommand "juggler-boundary" { } ''
            cd ${goSrc}
            # A missing tree would otherwise pass vacuously.
            test -d cmd/juggler && test -d internal/jugglerrun
            leaks=$(grep -rnoE --include='*.go' '"code\.linenisgreat\.com/clown/[^"]*"' . || true)
            if [ -n "$leaks" ]; then
              echo "juggler packages import clown packages (clown#258):" >&2
              echo "$leaks" >&2
              exit 1
            fi
            touch $out
          '';
        };

        devShells.default = pkgs.mkShell {
          packages = [
            conformistPkg
            # The config-specific, toolchain-hermetic hooks, on PATH as
            # `conformist-pre-commit` / `conformist-repair` so the sweatfile can
            # name them.
            eval.config.build.preCommit
            eval.config.build.repair
            justPkg
            # No ambient go: dependencies live in go.nix (igloo FDR 0008); go
            # commands run through godyn-go, tests through godyn-test.
            pkgs.godyn-go
            pkgs.godyn-test
          ];
        };
      }
    ))
    // {
      homeManagerModules.juggler = import ./nix/hm/juggler.nix;
    };
}
