# Bats integration lane via amarbel-llc/bats's batsLane builder. Stages
# zz-tests_bats/ into the build sandbox, exports binaries under stable env-var
# names, plumbs the bats-libs helper bundle onto BATS_LIB_PATH, and runs
# `bats --jobs N *.bats`. The lane consumes each binary by store path so it
# doesn't rebuild Go on filter changes. The binaries map comes from
# flake.nix's batsBinaries.
#
# Carried over from clown's bats.nix at the extraction (clown#258); juggler's
# three suites use no file tags, so there is a single lane.
{
  pkgs,
  batsLane,
  bats-libs,
  # The env var -> { base; name; } binaries map every lane exports
  # (flake.nix batsBinaries).
  batsBinaries,
}:
{
  bats-default = batsLane {
    filter = "";
    # Naming anchor only (`${base.pname}-bats-<suffix>`).
    base = batsBinaries.JUGGLER_BIN.base;
    batsSrc = ./zz-tests_bats;
    binaries = batsBinaries;
    # bats-libs ships bats-support, bats-assert, bats-emo, bats-island under
    # share/bats; surfacing batsLibPath here lets common.bash call
    # `bats_load_library bats-island` etc. from inside the lane.
    batsLibPath = [ bats-libs.batsLibPath ];
    nativeBuildInputs = with pkgs; [
      curl
      jq
      coreutils
      # bats-island's setup_test_home shells out to `git config` while
      # configuring GIT_CONFIG_GLOBAL. The nix builder PATH doesn't include
      # git by default, so provide it explicitly.
      git
    ];
  };
}
