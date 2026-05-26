# Loamss Homebrew formula.
#
# Source of truth lives in this repo. The companion tap repo at
# github.com/loamss/homebrew-loamss mirrors this file on every
# release so users can `brew tap loamss/loamss && brew install loamss`
# without needing to know the formula's exact URL.
#
# Mirror sync: a release-workflow step (.github/workflows/release.yml,
# update-homebrew-tap job) commits this file's updated version + SHAs
# to the tap repo automatically on every `v*` tag push.
#
# Auditing: the SHA256s here MUST match the .sha256 companion files
# uploaded alongside each release asset. The release workflow
# generates both from the same `shasum -a 256` invocation, so a
# mismatch indicates either a tampered tarball or a stale formula
# — investigate before merging.
class Loamss < Formula
  desc "Personal data infrastructure: one boundary for every AI tool"
  homepage "https://github.com/loamss/loamss"
  version "0.1.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/loamss/loamss/releases/download/v#{version}/loamss-v#{version}-darwin-arm64.tar.gz"
      sha256 "00d774db66eda8f7cbed4c57aedf4daadaf1f54933a2be7b7aa992bbbf272e69"
    end
    on_intel do
      url "https://github.com/loamss/loamss/releases/download/v#{version}/loamss-v#{version}-darwin-amd64.tar.gz"
      sha256 "f7c851f33e8860ebaea67d4a1e7a2f5d8459e52ac58e7c35850d4d3464df5d4a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/loamss/loamss/releases/download/v#{version}/loamss-v#{version}-linux-arm64.tar.gz"
      sha256 "865711c89f1e293236e7493f47eedc96ded91460b2feaa56488c40d013e139ba"
    end
    on_intel do
      url "https://github.com/loamss/loamss/releases/download/v#{version}/loamss-v#{version}-linux-amd64.tar.gz"
      sha256 "ad4bcd87fbd6a30699e836b012fa37e1a432b0bc8154927edfafe14bbc363baf"
    end
  end

  def install
    # The tarball contains a `loamss-vX.Y.Z-os-arch/` directory with
    # the binary plus LICENSE + README. Homebrew has already
    # extracted + cd'd into that directory by the time `install`
    # runs, so the binary is at ./loamss.
    bin.install "loamss"
    # Surface the bundled docs so `brew info loamss` and
    # `loamss --help` aren't the only hints a user has.
    doc.install "README.md", "LICENSE"
  end

  test do
    # Smoke test: the binary should at least report a version
    # without crashing. Anything more interactive would need a
    # temp data dir + permission to bind 127.0.0.1, which
    # Homebrew's bottle-test sandbox doesn't grant.
    assert_match version.to_s, shell_output("#{bin}/loamss version")
  end
end
