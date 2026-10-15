#!/bin/bash
set -e
BINARY_NAME="fuckyourcli"
INSTALL_DIR="/usr/local/bin"
SHARE_DIR="/usr/local/share/fyc"
SOURCE_BINARY="build/$BINARY_NAME"
# Function to configure AppArmor for bwrap (Ubuntu 24+)
configure_apparmor_bwrap() {
    [ "$(uname)" = "Linux" ] || return 0
    command -v apparmor_parser &> /dev/null || return 0
    if grep -q "Ubuntu" /etc/os-release 2>/dev/null; then
        VER=$(grep VERSION_ID /etc/os-release | cut -d'"' -f2 | cut -d'.' -f1)
        if [ "${VER:-0}" -ge 24 ] 2>/dev/null; then
            echo "Ubuntu 24+ detected: creating AppArmor profile for bwrap..."
            cat > /etc/apparmor.d/bwrap <<'EOF'
abi <abi/4.0>,
include <tunables/global>
profile bwrap /usr/bin/bwrap flags=(unconfined) {
  userns,
  include if exists <local/bwrap>
}
EOF
            systemctl reload apparmor
            echo "AppArmor bwrap profile created and loaded."
        fi
    fi
}
# --- EXECUTION LOGIC ---
# If not root, build and download local dependencies
if [ "$EUID" -ne 0 ]; then
    echo "Compiling..."
    ./build.sh
    echo "Build finished. Now installing system components with sudo..."
    sudo REAL_HOME="$HOME" FYC_CONFIG_DIR="${FYC_CONFIG_DIR:-}" XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-}" "$0"
    exit 0
fi
# If root (via sudo), install fyc binaries
if [ -f "$SOURCE_BINARY" ]; then
    # 1b. Fix AppArmor for bwrap on Ubuntu 24+
    configure_apparmor_bwrap
    
    # 2. Remove old fyc versions
    echo "Removing previous versions of $BINARY_NAME..."
    rm -f "$INSTALL_DIR/$BINARY_NAME"
    rm -rf "$SHARE_DIR"
    
    # 3. Install main binary
    echo "Installing $BINARY_NAME in $INSTALL_DIR..."
    cp "$SOURCE_BINARY" "$INSTALL_DIR/"
    chmod +x "$INSTALL_DIR/$BINARY_NAME"

    # 4. CREATE COMPLETION FILE (Executed as root)
    # The script comes from completions/ (single source of truth, also shipped
    # inside the release archives for setup.sh).
    BASH_COMP_DIR="/usr/share/bash-completion/completions"
    if [ ! -d "$BASH_COMP_DIR" ]; then
        echo "Warning: $BASH_COMP_DIR not found. Completion not installed system-wide."
    elif [ ! -f completions/fuckyourcli.bash ]; then
        echo "Warning: completions/fuckyourcli.bash not found. Completion not installed."
    else
        echo "Installing Bash completion in $BASH_COMP_DIR/$BINARY_NAME..."
        cp completions/fuckyourcli.bash "$BASH_COMP_DIR/$BINARY_NAME"
    fi

    # BEGIN fyc-config-dir (parsed by tools/app_paths_test.go)
    # 5. Use the same config directory as store.ConfigDir (not root's home).
    CONFIG_DIR="${FYC_CONFIG_DIR:-${XDG_CONFIG_HOME:-$REAL_HOME/.config}/fyc}"
    case "${XDG_CONFIG_HOME:-}" in
        /*|"") ;;
        *) [ -n "${FYC_CONFIG_DIR:-}" ] || CONFIG_DIR="$REAL_HOME/.config/fyc" ;;
    esac
    SKILLS_DIR="$CONFIG_DIR/skills"
    REAL_USER=$(stat -c '%U' "$REAL_HOME")
    # Copy as the real user: never change ownership of a shared config directory.
    echo "Copying skills to $SKILLS_DIR..."
    sudo -u "$REAL_USER" mkdir -p "$SKILLS_DIR"
    sudo -u "$REAL_USER" cp -r skills/* "$SKILLS_DIR/"
    # 5b. Install the models.dev catalog for the real user (config dir)
    MODELS_DIR="$CONFIG_DIR"
    if [ -f models/catalog.json ]; then
        echo "Copying models catalog to $MODELS_DIR/models.json..."
        sudo -u "$REAL_USER" mkdir -p "$MODELS_DIR"
        sudo -u "$REAL_USER" cp models/catalog.json "$MODELS_DIR/models.json"
    else
        echo "Warning: models/catalog.json not found. Catalog not installed."
    fi
    # END fyc-config-dir
    
    echo "--------------------------------------------------"
    echo "Installation completed successfully!"
    echo "You can use '$BINARY_NAME' from any directory."
else
    echo "Error: File $SOURCE_BINARY does not exist. Run the script without sudo first."
    exit 1
fi
