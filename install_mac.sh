#!/bin/bash
# install_mac.sh — install fuckyourcli on macOS.
# Build as current user and install the binary (sudo only if necessary).
set -e

BINARY_NAME="fuckyourcli"
SOURCE_BINARY="build/$BINARY_NAME"
# Installation directory (override with FYC_INSTALL_DIR=/path).
INSTALL_DIR="${FYC_INSTALL_DIR:-/usr/local/bin}"

# --- 1. macOS only ---
if [ "$(uname)" != "Darwin" ]; then
    echo "Error: install_mac.sh is intended for macOS. On Linux use ./install.sh." >&2
    exit 1
fi

# Real user home even if the script is launched with sudo.
REAL_HOME="$HOME"
if [ "$EUID" -eq 0 ] && [ -n "$SUDO_USER" ]; then
    REAL_HOME="$(eval echo "~$SUDO_USER")"
fi

# --- 2. Build as current user (never root) ---
echo "Building..."
./build.sh
if [ ! -f "$SOURCE_BINARY" ]; then
    echo "Error: $SOURCE_BINARY not found. Build failed." >&2
    exit 1
fi

# --- 3. Determine if sudo is needed for the installation directory ---
if [ ! -d "$INSTALL_DIR" ]; then
    mkdir -p "$INSTALL_DIR" 2>/dev/null || sudo mkdir -p "$INSTALL_DIR"
fi
SUDO=""
if [ ! -w "$INSTALL_DIR" ]; then
    SUDO="sudo"
fi
$SUDO true 2>/dev/null || { echo "Error: sudo required to write to $INSTALL_DIR." >&2; exit 1; }

# --- 4. Remove old versions ---
echo "Removing previous versions of $BINARY_NAME..."
$SUDO rm -f "$INSTALL_DIR/$BINARY_NAME"

# --- 5. Install main binary ---
echo "Installing $BINARY_NAME in $INSTALL_DIR..."
$SUDO cp "$SOURCE_BINARY" "$INSTALL_DIR/"
$SUDO chmod +x "$INSTALL_DIR/$BINARY_NAME"

# --- 6. Gatekeeper: remove quarantine attribute (no-op if absent) ---
xattr -dr com.apple.quarantine "$INSTALL_DIR/$BINARY_NAME" 2>/dev/null || true

# --- 7. Bash completion ---
# On macOS bash-completion is not system-provided: try Homebrew paths.
BASH_COMP_DIR=""
for d in \
    "${HOMEBREW_PREFIX:-/opt/homebrew}/etc/bash_completion.d" \
    "/usr/local/etc/bash_completion.d" \
    "/usr/share/bash-completion/completions"
do
    if [ -d "$d" ]; then BASH_COMP_DIR="$d"; break; fi
done
if [ -n "$BASH_COMP_DIR" ]; then
    echo "Installing Bash completion in $BASH_COMP_DIR/$BINARY_NAME..."
    BSUDO=""
    [ -w "$BASH_COMP_DIR" ] || BSUDO="sudo"
    if [ -f completions/fuckyourcli.bash ]; then
        $BSUDO cp completions/fuckyourcli.bash "$BASH_COMP_DIR/$BINARY_NAME"
    else
        echo "Warning: completions/fuckyourcli.bash not found. Completion not installed."
    fi
else
    echo "Warning: no bash-completion directory found (install 'bash-completion' with Homebrew)."
fi

# --- 8. Zsh completion (default shell on macOS) ---
# Copying _fuckyourcli is not enough: zsh loads completions only from directories
# listed in $fpath, and ~/.zsh/completions is not part of it. It must be registered in
# ~/.zshrc (unlike bash, which reads /usr/share/bash-completion on its own).
ZSH_COMP_DIR="$REAL_HOME/.zsh/completions"
ZSHRC="$REAL_HOME/.zshrc"
if command -v zsh >/dev/null 2>&1; then
    echo "Installing Zsh completion in $ZSH_COMP_DIR/_$BINARY_NAME..."
    mkdir -p "$ZSH_COMP_DIR"
    if [ -f completions/_fuckyourcli ]; then
        cp completions/_fuckyourcli "$ZSH_COMP_DIR/_$BINARY_NAME"
    else
        echo "Warning: completions/_fuckyourcli not found. Completion not installed."
    fi
    # Idempotent registration in ~/.zshrc: without the fpath+= line and a subsequent
    # compinit, zsh won't see the file and Tab will remain silent.
    if grep -qF "$ZSH_COMP_DIR" "$ZSHRC" 2>/dev/null; then
        echo "  Completion already registered in ~/.zshrc."
    else
        {
            echo ""
            echo "# fuckyourcli: zsh completion"
            echo "fpath+=(\"$ZSH_COMP_DIR\")"
            echo "autoload -Uz compinit && compinit"
        } >> "$ZSHRC"
        echo "  Registered in ~/.zshrc: restart terminal to activate Tab."
    fi
else
    echo "Warning: zsh not found. Zsh completion not installed."
fi

# BEGIN fyc-config-dir (parsed by tools/app_paths_test.go)
# --- 9. Skills in the native config directory ---
CONFIG_DIR="${FYC_CONFIG_DIR:-$REAL_HOME/Library/Application Support/fyc}"
SKILLS_DIR="$CONFIG_DIR/skills"
if [ -d skills ]; then
    echo "Copying skills to $SKILLS_DIR..."
    mkdir -p "$SKILLS_DIR"
    cp -r skills/* "$SKILLS_DIR/"
fi

# --- 9b. models.dev catalog (config dir) ---
MODELS_DIR="$CONFIG_DIR"
if [ -f models/catalog.json ]; then
    echo "Copying models catalog to $MODELS_DIR/models.json..."
    mkdir -p "$MODELS_DIR"
    cp models/catalog.json "$MODELS_DIR/models.json"
fi
# END fyc-config-dir

# --- 10. Verify PATH ---
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)  echo "Warning: $INSTALL_DIR is not in PATH. Add to your shell rc:"
        echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

echo "--------------------------------------------------"
echo "Installation completed successfully!"
echo "You can use '$BINARY_NAME' from any directory."
