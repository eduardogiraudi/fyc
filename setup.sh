#!/bin/sh
# setup.sh — download and install a prebuilt fuckyourcli release (no Go toolchain).
#
#   curl -fsSL https://raw.githubusercontent.com/eduardogiraudi/fyc/master/setup.sh | sh -s -- [options]
#
# Use install.sh instead when you want to install from the local sources
# (it builds with build.sh and configures the system bits with sudo).
set -eu

REPO="eduardogiraudi/fyc"
BIN="fuckyourcli"

VERSION=""
INSTALL_DIR=""
# Install system-wide (/usr/local/bin) by default; --user switches to
# ~/.local/bin. sudo is used only when the target directory is not writable.
SYSTEM=1
DRY_RUN=0
FORCE=0

usage() {
	cat <<'EOF'
setup.sh — download and install a prebuilt fuckyourcli release

Usage:
  setup.sh [options]
  curl -fsSL <raw-url>/setup.sh | sh -s -- [options]

Options:
  -v, --version <tag>   install a specific version (0.1.0 or v0.1.0)
  -d, --dir <path>      install directory (default: /usr/local/bin)
      --system          install into /usr/local/bin (default; uses sudo if needed)
      --user            install into ~/.local/bin without sudo
  -n, --dry-run         print what would be downloaded, install nothing
  -f, --force           reinstall even if the same version is already installed
      --no-skills       do not install the bundled skills
      --no-completion   do not install the shell completion
  -h, --help            show this help

Environment:
  FYC_INSTALL_DIR       same as --dir
EOF
}

die() {
	echo "setup.sh: error: $*" >&2
	exit 1
}

warn() {
	echo "setup.sh: warning: $*" >&2
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "'$1' is required but was not found in PATH"
}

fetch() {
	curl -fsSL "$@"
}

while [ $# -gt 0 ]; do
	case "$1" in
	-v | --version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION="$2"
		shift 2
		;;
	-d | --dir | --prefix)
		[ $# -ge 2 ] || die "--dir needs a value"
		INSTALL_DIR="$2"
		shift 2
		;;
	--system)
		SYSTEM=1
		shift
		;;
	--user)
		SYSTEM=0
		shift
		;;
	-n | --dry-run)
		DRY_RUN=1
		shift
		;;
	-f | --force)
		FORCE=1
		shift
		;;
	--no-skills)
		NO_SKILLS=1
		shift
		;;
	--no-completion)
		NO_COMPLETION=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		die "unknown option: $1"
		;;
	esac
done

need curl
need tar

# ── platform ─────────────────────────────────────────────────────────────────
# goreleaser names the assets `<project>_<version>_<os>_<arch>.tar.gz` with the
# Go os/arch (linux/amd64, darwin/arm64); the matcher below also accepts the
# "friendly" variants (Linux/x86_64, Darwin/arm64).
case "$(uname -s)" in
	Linux) OS="Linux" ;;
	Darwin) OS="Darwin" ;;
	*) die "unsupported OS '$(uname -s)': on Windows use setup.ps1" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64) ARCH="x86_64" ;;
	aarch64 | arm64) ARCH="arm64" ;;
	*) die "unsupported architecture '$(uname -m)'" ;;
esac

# Asset-name variants. goreleaser writes the Go os/arch (linux/amd64) with the
# current config; the "friendly" names (Linux/x86_64) are accepted too, so the
# matcher survives a name-template change in either direction.
OS_RE="$OS"
case "$OS" in
	Linux) OS_RE="Linux|linux" ;;
	Darwin) OS_RE="Darwin|darwin" ;;
esac
ARCH_RE="$ARCH"
case "$ARCH" in
	x86_64) ARCH_RE="x86_64|amd64" ;;
	arm64) ARCH_RE="arm64|aarch64" ;;
esac

if [ -z "$INSTALL_DIR" ]; then
	if [ "$SYSTEM" = 1 ]; then
		INSTALL_DIR="${FYC_INSTALL_DIR:-/usr/local/bin}"
	else
		INSTALL_DIR="${FYC_INSTALL_DIR:-$HOME/.local/bin}"
	fi
fi
DEST="$INSTALL_DIR/$BIN"

# ── release metadata ─────────────────────────────────────────────────────────
API="https://api.github.com/repos/$REPO/releases"
if [ -n "$VERSION" ]; then
	case "$VERSION" in
	v*) TAG="$VERSION" ;;
	*) TAG="v$VERSION" ;;
	esac
	META=$(fetch "$API/tags/$TAG") || die "cannot fetch release $TAG"
else
	META=$(fetch "$API/latest") || die "cannot fetch the latest release"
	TAG=$(printf '%s\n' "$META" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
	[ -n "$TAG" ] || die "the release metadata has no tag_name"
fi

ASSET_URLS=$(printf '%s\n' "$META" |
	grep '"browser_download_url"' |
	sed -n 's|.*"\([a-z][a-z0-9+.-]*://[^"]*\)".*|\1|p')
ASSET_URL=$(printf '%s\n' "$ASSET_URLS" |
	grep -E "_(${OS_RE})_(${ARCH_RE})\.tar\.gz$" |
	head -n1)
if [ -z "$ASSET_URL" ]; then
	# Show what the release actually ships instead of failing blind.
	if [ -n "$ASSET_URLS" ]; then
		echo "setup.sh: assets in $TAG:" >&2
		printf '%s\n' "$ASSET_URLS" | sed 's|.*/||; s|^|  - |' >&2
	fi
	die "release $TAG has no ${OS}/${ARCH} archive"
fi
ASSET=$(basename "$ASSET_URL")

echo "fuckyourcli $TAG ($OS/$ARCH)"
echo "  installer: $ASSET"
echo "  target:    $DEST"

if [ "$DRY_RUN" = 1 ]; then
	echo "  url:       $ASSET_URL"
	echo "dry run: nothing downloaded or installed"
	exit 0
fi

# Nothing to do when the wanted version is already installed. A binary built
# without -X main.version reports "dev", so the check simply never matches.
if [ "$FORCE" = 0 ] && [ -x "$DEST" ]; then
	CURRENT=$("$DEST" --version 2>/dev/null | sed -n 's/^v\([^ ]*\).*/\1/p' || true)
	if [ "$CURRENT" = "${TAG#v}" ]; then
		echo "already installed: $DEST (use --force to reinstall)"
		exit 0
	fi
fi

# ── download + verify ────────────────────────────────────────────────────────
WORK=$(mktemp -d "${TMPDIR:-/tmp}/fyc-setup.XXXXXX") || die "cannot create a temporary directory"
trap 'rm -rf "$WORK"' EXIT INT TERM

echo "downloading $ASSET..."
fetch -o "$WORK/$ASSET" "$ASSET_URL" || die "download failed"

if fetch -o "$WORK/checksums.txt" "https://github.com/$REPO/releases/download/$TAG/checksums.txt" 2>/dev/null; then
	LINE=$(grep -E "[[:space:]]${ASSET}\$" "$WORK/checksums.txt" | head -n1 || true)
	if [ -z "$LINE" ]; then
		die "checksums.txt has no entry for $ASSET"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		( cd "$WORK" && printf '%s\n' "$LINE" | sha256sum -c - ) || die "checksum mismatch for $ASSET"
	elif command -v shasum >/dev/null 2>&1; then
		( cd "$WORK" && printf '%s\n' "$LINE" | shasum -a 256 -c - ) || die "checksum mismatch for $ASSET"
	else
		warn "no sha256sum/shasum found: skipping checksum verification"
	fi
	echo "checksum verified"
else
	warn "the release has no checksums.txt: skipping checksum verification"
fi

# ── extract + install ────────────────────────────────────────────────────────
mkdir -p "$WORK/extract"
tar -xzf "$WORK/$ASSET" -C "$WORK/extract" || die "cannot extract $ASSET"
SRC="$WORK/extract/$BIN"
[ -f "$SRC" ] || die "the archive does not contain '$BIN'"
chmod 0755 "$SRC"

# install_bin copies the binary with the executable bit set; `install` is not
# guaranteed to exist (busybox), so fall back to cp+chmod.
install_bin() {
	if command -v install >/dev/null 2>&1; then
		install -m 0755 "$1" "$2"
	else
		cp "$1" "$2" && chmod 0755 "$2"
	fi
}

# The target directory decides whether sudo is needed: /usr/local/bin (the
# default) is root-owned, a user directory is not.
if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
	need sudo
	sudo mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
fi
SUDO=""
[ -w "$INSTALL_DIR" ] || SUDO="sudo"
if [ -n "$SUDO" ]; then
	need sudo
	$SUDO cp "$SRC" "$DEST" || die "cannot install into $INSTALL_DIR"
	$SUDO chmod 0755 "$DEST"
else
	install_bin "$SRC" "$DEST" || die "cannot write $DEST"
fi

if [ "$OS" = "Darwin" ]; then
	# Gatekeeper: curl downloads carry no quarantine attribute (no-op safe)
	xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
fi

echo "installed: $("$DEST" --version 2>/dev/null || echo "$BIN $TAG")"

case ":${PATH}:" in
*":${INSTALL_DIR}:"*) ;;
*)
	echo
	echo "note: $INSTALL_DIR is not in your PATH. Add it with:"
	echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
	;;
esac

# ── post-install configuration (same steps as the source installers) ─────────
# AppArmor profile for bwrap (Ubuntu 24+), required by the sandboxed `sh`.
# Applied on every Linux install, not only with --system: without it the
# sandbox cannot start (Ubuntu blocks unprivileged user namespaces).
configure_apparmor_bwrap() {
	[ "$OS" = "Linux" ] || return 0
	command -v apparmor_parser >/dev/null 2>&1 || return 0
	[ -r /etc/os-release ] || return 0
	grep -q "Ubuntu" /etc/os-release || return 0
	VER=$(sed -n 's/^VERSION_ID="\?\([0-9][0-9]*\).*/\1/p' /etc/os-release | head -n1)
	[ "${VER:-0}" -ge 24 ] 2>/dev/null || return 0
	if ! command -v sudo >/dev/null 2>&1; then
		warn "sudo not found: create /etc/apparmor.d/bwrap manually for the sandboxed 'sh'"
		return 0
	fi
	echo "configuring the AppArmor profile for bwrap (Ubuntu $VER)..."
	if ! sudo tee /etc/apparmor.d/bwrap >/dev/null <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>

profile bwrap /usr/bin/bwrap flags=(unconfined) {
  userns,

  include if exists <local/bwrap>
}
PROFILE
	then
		warn "cannot write /etc/apparmor.d/bwrap: create the bwrap AppArmor profile manually"
		return 0
	fi
	sudo systemctl reload apparmor || warn "cannot reload apparmor (run: sudo systemctl reload apparmor)"
}

# BEGIN fyc-config-dir (parsed by tools/app_paths_test.go)
REAL_HOME="$HOME"
if [ -n "${FYC_CONFIG_DIR:-}" ]; then
	CONFIG_DIR="$FYC_CONFIG_DIR"
elif [ "$OS" = "Darwin" ]; then
	CONFIG_DIR="$REAL_HOME/Library/Application Support/fyc"
else
	case "${XDG_CONFIG_HOME:-}" in
		/*) CONFIG_DIR="$XDG_CONFIG_HOME/fyc" ;;
		*) CONFIG_DIR="$REAL_HOME/.config/fyc" ;;
	esac
fi
EXTRACT="$WORK/extract"

if [ "${NO_SKILLS:-0}" = 1 ]; then
	echo "skills: skipped"
elif [ -d "$EXTRACT/skills" ]; then
	mkdir -p "$CONFIG_DIR/skills"
	cp -R "$EXTRACT/skills/." "$CONFIG_DIR/skills/"
	echo "skills: $CONFIG_DIR/skills"
else
	warn "the archive has no skills/ directory: skipping"
fi

if [ -f "$EXTRACT/models/catalog.json" ]; then
	mkdir -p "$CONFIG_DIR"
	cp "$EXTRACT/models/catalog.json" "$CONFIG_DIR/models.json"
	echo "catalog: $CONFIG_DIR/models.json"
else
	warn "the archive has no models/catalog.json: skipping"
fi
# END fyc-config-dir

bash_completion_target() {
	# bash-completion auto-loads these directories: prefer the system one and
	# fall back to the XDG user directory when none of them exists.
	DIRS="/usr/share/bash-completion/completions /usr/local/etc/bash_completion.d"
	if [ "$OS" = "Darwin" ]; then
		DIRS="${HOMEBREW_PREFIX:-/opt/homebrew}/etc/bash_completion.d $DIRS"
	fi
	for d in $DIRS; do
		if [ -d "$d" ]; then
			echo "$d"
			return
		fi
	done
	echo "${XDG_DATA_HOME:-$REAL_HOME/.local/share}/bash-completion/completions"
}

install_bash_completion() {
	COMP_FILE="$EXTRACT/completions/fuckyourcli.bash"
	if [ ! -f "$COMP_FILE" ]; then
		warn "the archive has no completions/fuckyourcli.bash: skipping"
		return 0
	fi
	COMP_DIR=$(bash_completion_target)
	CSUDO=""
	[ -w "$COMP_DIR" ] || CSUDO="sudo"
	[ -z "$CSUDO" ] || need sudo
	$CSUDO mkdir -p "$COMP_DIR" || die "cannot create $COMP_DIR"
	$CSUDO cp "$COMP_FILE" "$COMP_DIR/$BIN" || die "cannot install the completion in $COMP_DIR"
	echo "completion: $COMP_DIR/$BIN"
}

install_zsh_completion() {
	# zsh reads completions only from $fpath, so ~/.zsh/completions is registered in ~/.zshrc
	if ! command -v zsh >/dev/null 2>&1 || [ ! -f "$EXTRACT/completions/_fuckyourcli" ]; then
		warn "zsh not found or no completion in the archive: skipping"
		return 0
	fi
	mkdir -p "$REAL_HOME/.zsh/completions"
	cp "$EXTRACT/completions/_fuckyourcli" "$REAL_HOME/.zsh/completions/_fuckyourcli"
	if grep -qF "$REAL_HOME/.zsh/completions" "$REAL_HOME/.zshrc" 2>/dev/null; then
		echo "completion: $REAL_HOME/.zsh/completions/_fuckyourcli (already registered)"
	else
		{
			echo ""
			echo "# fuckyourcli: zsh completion"
			echo "fpath+=(\"$REAL_HOME/.zsh/completions\")"
			echo "autoload -Uz compinit && compinit"
		} >>"$REAL_HOME/.zshrc"
		echo "completion: $REAL_HOME/.zsh/completions/_fuckyourcli (registered in ~/.zshrc)"
	fi
}

if [ "${NO_COMPLETION:-0}" = 1 ]; then
	echo "completion: skipped"
elif [ "$OS" = "Darwin" ]; then
	install_zsh_completion
	install_bash_completion
else
	install_bash_completion
fi

# Applied unconditionally on Linux: a no-op elsewhere, on non-Ubuntu, or on
# Ubuntu < 24 (the function checks all of that itself).
configure_apparmor_bwrap
