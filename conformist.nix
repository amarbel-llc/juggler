# This repo's conformist overlay, merged with conformist.lib.presets.eng in
# flake.nix. The preset enables the eng-convention linters; here you choose the
# formatters and any repo-specific tweaks.
{ ... }:
{
  # Formatters this repo wants. nixfmt formats the flake itself; add your
  # language's formatter (gofmt, rustfmt, prettier, …) — see `man conformist.toml`
  # and the conformist programs registry.
  programs.nixfmt.enable = true;

  # eng-versioning(7) derives the version key from go.mod / go.nix / Cargo.toml;
  # go.nix names no key (go.mod is rendered inside nix), so set it explicitly;
  # it must match version.env.
  linters.eng-versioning.key = "JUGGLER_VERSION";

  # Prose and generated files are out of scope for code formatters.
  settings.excludes = [
    # The dagnabit facade is generated and formatted by dagnabit itself.
    "pkgs/**"
    "*.md"
    "flake.lock"
    "LICENSE"
  ];
}
