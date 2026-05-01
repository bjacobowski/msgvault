#!/usr/bin/env bash
# WSL Ubuntu dev environment setup for msgvault-omgnos.
#
# Installs the toolchain managers (Nix + direnv + nix-direnv), wires the
# shell hooks, and allows direnv in the repo so `cd`-ing in auto-loads
# Go 1.25.9, golangci-lint, gcc, and prek from the flake.
#
# Idempotent: every step is gated by an "already installed?" check, so
# re-running it after a partial failure is safe.
#
# Prerequisites:
#   - WSL Ubuntu 24.04 (tested) with sudo, curl, git, gcc, make installed
#   - The msgvault repo cloned into WSL's native filesystem (NOT /mnt/c).
#     If you haven't cloned it yet:
#         git clone -b omgnos https://github.com/bjacobowski/msgvault.git \
#             ~/github.com/bjacobowski/msgvault
#         cd ~/github.com/bjacobowski/msgvault
#
# Usage (from inside the repo):
#   bash scripts/setup-wsl-dev.sh

set -euo pipefail

say()  { printf '\n=== %s ===\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"

if [[ ! -f flake.nix ]]; then
    echo "Error: $REPO_DIR doesn't look like the msgvault repo (no flake.nix)" >&2
    exit 1
fi

# Refuse to run from /mnt/c — the whole point is to use WSL's ext4.
case "$REPO_DIR" in
    /mnt/c/*|/mnt/d/*)
        echo "Error: repo is at $REPO_DIR (NTFS via 9P)." >&2
        echo "Clone into WSL's native filesystem first; see header for the command." >&2
        exit 1
        ;;
esac

# --- 1. Nix (Determinate Systems installer) -----------------------------------
say "Nix"
if have nix; then
    echo "Already installed: $(nix --version)"
else
    # --determinate enables their managed install (auto-updates, simpler
    # uninstall via `/nix/nix-installer uninstall`).
    # --no-confirm skips the interactive prompt — you're reading this script,
    # so you've already confirmed.
    curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix \
        | sh -s -- install linux --determinate --no-confirm
fi

# Source Nix's profile so subsequent steps in THIS shell see `nix` on PATH.
# Determinate installs to /nix/var/nix/profiles/default; the script below
# gets sourced from /etc/bash.bashrc on new shells.
if ! have nix; then
    if [[ -f /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh ]]; then
        # shellcheck disable=SC1091
        . /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh
    fi
fi
have nix || { echo "Nix install completed but 'nix' not on PATH; open a new shell and re-run." >&2; exit 1; }

# --- 2. direnv (apt) ----------------------------------------------------------
say "direnv"
if have direnv; then
    echo "Already installed: $(direnv --version)"
else
    sudo apt-get update
    sudo apt-get install -y direnv
fi

# --- 3. nix-direnv (via Nix profile) ------------------------------------------
say "nix-direnv"
NIX_DIRENV_RC="$HOME/.nix-profile/share/nix-direnv/direnvrc"
if [[ -f "$NIX_DIRENV_RC" ]]; then
    echo "Already installed: $NIX_DIRENV_RC"
else
    nix profile install nixpkgs#nix-direnv
fi

# --- 4. Shell hooks -----------------------------------------------------------
say "~/.bashrc hook"
BASHRC="$HOME/.bashrc"
HOOK_MARKER="# >>> msgvault dev env (direnv) >>>"
if grep -qF "$HOOK_MARKER" "$BASHRC" 2>/dev/null; then
    echo "Hook already in $BASHRC"
else
    cat >> "$BASHRC" <<'EOF'

# >>> msgvault dev env (direnv) >>>
# Auto-load .envrc files when entering a directory.
# Safety: direnv only loads .envrc files you've explicitly approved with
# `direnv allow`, so cloning a hostile repo can't run code on `cd`.
eval "$(direnv hook bash)"
# <<< msgvault dev env (direnv) <<<
EOF
    echo "Appended direnv hook to $BASHRC"
fi

say "~/.config/direnv/direnvrc"
mkdir -p "$HOME/.config/direnv"
DIRENVRC="$HOME/.config/direnv/direnvrc"
if [[ -f "$DIRENVRC" ]] && grep -qF nix-direnv "$DIRENVRC"; then
    echo "nix-direnv already sourced from $DIRENVRC"
else
    # nix-direnv provides a fast `use flake` — caches the dev shell so
    # `cd` into the repo is instant after the first build.
    cat > "$DIRENVRC" <<'EOF'
source "$HOME/.nix-profile/share/nix-direnv/direnvrc"
EOF
    echo "Wrote $DIRENVRC"
fi

# --- 5. direnv allow in the repo ----------------------------------------------
say "direnv allow $REPO_DIR"
direnv allow .

# --- 6. Smoke test ------------------------------------------------------------
say "Smoke test (this builds the dev shell — first run takes a few minutes)"
nix develop --command bash -c '
    echo "Go:            $(go version)"
    echo "golangci-lint: $(golangci-lint --version 2>&1 | head -1)"
    echo "gcc:           $(gcc --version | head -1)"
    echo "prek:          $(prek --version 2>&1 | head -1)"
'

cat <<EOF

=== Done ===

Open a NEW shell (or run 'exec bash') so the direnv hook takes effect.
Then in any new shell:

    cd $REPO_DIR
    # direnv: loading ~/github.com/bjacobowski/msgvault/.envrc
    which go            # /nix/store/...go-1.25.9/bin/go
    make build          # produces msgvault-omgnos

To deactivate: cd out of the repo. Nothing is system-wide.
EOF
