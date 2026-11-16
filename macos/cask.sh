#!/bin/sh
# Prints the Homebrew cask for a release of the Mac app, for the tap
# (jflowers/homebrew-tools, Casks/gh-hud.rb). The release workflow runs it;
# by hand:
#
#   macos/cask.sh 0.1.0 "$(shasum -a 256 build/gh-hud-app_0.1.0.zip | cut -d' ' -f1)"
set -eu
version=${1:?usage: cask.sh VERSION SHA256}
sha=${2:?usage: cask.sh VERSION SHA256}
cat <<EOF
# Written by gh-hud's release workflow (macos/cask.sh). Edits are overwritten.
cask "gh-hud" do
  version "$version"
  sha256 "$sha"

  url "https://github.com/jflowers/gh-hud/releases/download/v#{version}/gh-hud-app_#{version}.zip"
  name "gh-hud"
  desc "Dashboard for GitHub work and coding agents, a client of the gh-hud hub"
  homepage "https://github.com/jflowers/gh-hud"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :sonoma

  app "gh-hud.app"
end
EOF
