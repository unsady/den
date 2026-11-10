#!/bin/sh
# Installs den and starts its setup:
#
#   curl -fsSL https://raw.githubusercontent.com/unsady/den/main/install.sh | sh
#
# DEN_VERSION picks a release, such as v0.1.0, instead of the latest.
# DEN_BIN_DIR is where den goes, ~/.local/bin by default.
set -eu

repo=${DEN_REPO:-unsady/den}
version=${DEN_VERSION:-latest}
bin_dir=${DEN_BIN_DIR:-$HOME/.local/bin}

fail() {
	printf 'den: %s\n' "$*" >&2
	exit 1
}

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "den runs on macOS and Linux" ;;
esac
case $(uname -m) in
arm64 | aarch64) arch=arm64 ;;
x86_64 | amd64) arch=amd64 ;;
*) fail "den has no build for $(uname -m)" ;;
esac

# den runs the agent in a container, so it needs a tool to run one.
has() { command -v "$1" >/dev/null 2>&1; }
if [ "$os" = darwin ]; then
	has container || has docker ||
		fail "install Apple's container from https://github.com/apple/container/releases, or Docker, then run this again"
else
	has docker || has podman || fail "install Docker or Podman, then run this again"
fi

if [ "$version" = latest ]; then
	base=${DEN_DOWNLOAD:-https://github.com/$repo/releases/latest/download}
else
	base=${DEN_DOWNLOAD:-https://github.com/$repo/releases/download/$version}
fi
file=den-$os-$arch

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading den for $os/$arch..."
curl -fsSL -o "$tmp/den" "$base/$file" || fail "could not download $base/$file"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || fail "could not download $base/SHA256SUMS"

want=$(awk -v f="$file" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
if has sha256sum; then
	got=$(sha256sum "$tmp/den" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/den" | awk '{ print $1 }')
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "the download does not match its checksum"

mkdir -p "$bin_dir"
cp "$tmp/den" "$bin_dir/den"
chmod 755 "$bin_dir/den"
echo "Installed $bin_dir/den"
case ":$PATH:" in
*":$bin_dir:"*) ;;
*) echo "Add $bin_dir to your PATH to run den from anywhere." ;;
esac

# The setup asks questions, so it reads the terminal, not this script.
if [ -r /dev/tty ] && [ -z "${DEN_NO_SETUP:-}" ]; then
	echo
	"$bin_dir/den" setup </dev/tty
else
	echo "Run den setup to finish."
fi
