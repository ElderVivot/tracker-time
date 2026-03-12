#!/bin/bash
# Instala a extensão GNOME Shell do tracker-time.
# Uso: bash install.sh
set -e

EXT_UUID="tracker-time@autmais"
EXT_DIR="$HOME/.local/share/gnome-shell/extensions/$EXT_UUID"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$EXT_DIR"
cp "$SCRIPT_DIR/metadata.json" "$EXT_DIR/"
cp "$SCRIPT_DIR/extension.js" "$EXT_DIR/"

echo "Extensão instalada em: $EXT_DIR"
echo ""
echo "Para ativar, execute:"
echo "  gnome-extensions enable $EXT_UUID"
echo ""
echo "Se não funcionar, reinicie a sessão GNOME (logout/login) e tente novamente."
