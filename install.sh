#!/bin/sh
# Installs the jev-check binary from a GitHub release. Go is not needed.
#
#   curl -fsSL https://raw.githubusercontent.com/luisferrassini/jev-check/main/install.sh | sh
#
# Settings, all optional:
#   JEV_CHECK_VERSION=v0.1.0      a release tag (default: the latest release)
#   JEV_CHECK_INSTALL_DIR=DIR     where the binary goes (default: ~/.local/bin)
#   JEV_CHECK_NO_MODIFY_PATH=1    never edit a shell startup file
#   JEV_CHECK_RELEASE_URL=URL     where the release files are (default: GitHub)
set -eu

repo="luisferrassini/jev-check"
version="${JEV_CHECK_VERSION:-latest}"
dir="${JEV_CHECK_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "no prebuilt binary for $(uname -s); build from source with: go install github.com/$repo@latest" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no prebuilt binary for $(uname -m); build from source with: go install github.com/$repo@latest" ;;
esac
asset="jev-check_${os}_${arch}"

if [ -n "${JEV_CHECK_RELEASE_URL:-}" ]; then
	base="$JEV_CHECK_RELEASE_URL"
elif [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi

download() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "needs curl or wget"
	fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset ($version)"
download "$base/$asset" "$tmp/$asset" || fail "could not download $base/$asset"
download "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download $base/checksums.txt"

want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
	got="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
else
	fail "needs sha256sum or shasum to verify the download"
fi
[ -n "$want" ] || fail "checksums.txt has no entry for $asset"
[ "$got" = "$want" ] || fail "checksum mismatch for $asset: got $got, want $want"

mkdir -p "$dir"
chmod 755 "$tmp/$asset"
mv -f "$tmp/$asset" "$dir/jev-check"
echo "Installed $dir/jev-check"

# Put dir on PATH for new shells, once, unless it is already there.
case ":$PATH:" in
*":$dir:"*) on_path=1 ;;
*) on_path=0 ;;
esac
if [ "$on_path" = 0 ]; then
	case "$(basename "${SHELL:-sh}")" in
	zsh) rc="${ZDOTDIR:-$HOME}/.zshrc" line="export PATH=\"$dir:\$PATH\"" ;;
	bash) rc="$HOME/.bashrc" line="export PATH=\"$dir:\$PATH\"" ;;
	fish) rc="$HOME/.config/fish/config.fish" line="fish_add_path \"$dir\"" ;;
	*) rc="$HOME/.profile" line="export PATH=\"$dir:\$PATH\"" ;;
	esac
	if [ "${JEV_CHECK_NO_MODIFY_PATH:-}" = 1 ]; then
		echo "$dir is not on your PATH. Add this line to $rc:"
		echo "  $line"
	elif [ -f "$rc" ] && grep -qF "$line" "$rc"; then
		echo "$rc already adds $dir to PATH. Open a new terminal to use it."
	else
		mkdir -p "$(dirname "$rc")"
		printf '\n# Added by the jev-check installer\n%s\n' "$line" >>"$rc"
		echo "Added $dir to PATH in $rc. Open a new terminal, or run:"
		echo "  $line"
	fi
fi

# An older jev-check earlier on PATH, such as one from go install, would hide this one.
# The line added above puts dir first, so this matters only when dir was on PATH already.
found="$(command -v jev-check 2>/dev/null || true)"
if [ "$on_path" = 1 ] && [ -n "$found" ] && [ "$found" != "$dir/jev-check" ]; then
	echo "Warning: $found comes first on your PATH and hides $dir/jev-check. Delete it with: rm '$found'"
fi

echo "Next, in the root of a Git project: jev-check init"
