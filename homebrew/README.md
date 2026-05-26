# Homebrew distribution

This directory holds the canonical Loamss Homebrew formula. The
formula targets both **macOS** (arm64 + amd64) and **Linux**
(arm64 + amd64, via Linuxbrew) — Homebrew has been Linux-native
since 2019.

## For users

Once the companion tap repo at `loamss/homebrew-loamss` is set
up (see "Tap setup" below), installation is one line:

```bash
brew tap loamss/loamss
brew install loamss
```

Or, equivalently:

```bash
brew install loamss/loamss/loamss
```

Until the tap is published, install directly from the formula's
canonical URL in this repo:

```bash
brew install --formula \
  https://raw.githubusercontent.com/loamss/loamss/main/homebrew/Formula/loamss.rb
```

After install, `loamss start --open` brings up the first-run
wizard at `http://127.0.0.1:7777`.

## Verifying a release

Every release ships `.sha256` companion files alongside each
binary tarball. To verify before installing:

```bash
VER=v0.1.0
curl -L -O https://github.com/loamss/loamss/releases/download/$VER/loamss-$VER-darwin-arm64.tar.gz
curl -L -O https://github.com/loamss/loamss/releases/download/$VER/loamss-$VER-darwin-arm64.tar.gz.sha256
shasum -a 256 -c loamss-$VER-darwin-arm64.tar.gz.sha256
```

The same SHAs appear in `Formula/loamss.rb`. A mismatch means
either a tampered tarball or a stale formula — open an issue
before installing.

## Tap setup (maintainers only)

A Homebrew tap is just a GitHub repo named `homebrew-<name>` that
contains a `Formula/` directory. Steps to publish the tap for the
first time:

1. Create `github.com/loamss/homebrew-loamss` (public repo).
2. Copy `homebrew/Formula/loamss.rb` from this repo to the root
   of the tap repo at `Formula/loamss.rb`.
3. Commit + push. The tap is now installable as
   `brew tap loamss/loamss`.

After the tap exists, the main repo's release workflow keeps it
in sync: on every `v*` tag push, the `update-homebrew-tap` job
recomputes the SHA256s + commits the new formula to the tap repo
using a `HOMEBREW_TAP_TOKEN` secret (a GitHub PAT with `contents:
write` on `loamss/homebrew-loamss`).

Until the tap exists, the workflow no-ops the sync step (logs a
warning + continues). The formula in this repo is still
authoritative; the tap is just a convenience mirror so
`brew tap` resolves correctly.

## Why this layout (formula in main repo + mirror to tap)

- **One source of truth.** PRs that bump SHAs land in the main
  repo where the rest of the release process happens. No second
  repo to keep in sync by hand.
- **Reviewable diffs.** Formula changes show up in main-repo
  history alongside the code change that triggered them.
- **Tap is auto-generated.** Maintainers never edit the tap repo
  directly; the sync job handles it.
- **Brew install works either way.** Users who prefer the
  one-line URL form get exactly the same artifact as users who
  tap.
