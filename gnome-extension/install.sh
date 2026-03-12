#!/bin/bash
# Instala a extensão GNOME Shell do tracker-time.
# Uso:
#   bash install.sh           # instala para o usuário atual (~/.local/share/...)
#   sudo bash install.sh --global  # instala para todos os usuários (/usr/share/...)
set -e

EXT_UUID="tracker-time@autmais"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if [ "$1" = "--global" ]; then
  EXT_DIR="/usr/share/gnome-shell/extensions/$EXT_UUID"
else
  EXT_DIR="$HOME/.local/share/gnome-shell/extensions/$EXT_UUID"
fi

mkdir -p "$EXT_DIR"
cp "$SCRIPT_DIR/metadata.json" "$EXT_DIR/"
cp "$SCRIPT_DIR/extension.js" "$EXT_DIR/"

echo "Extensão instalada em: $EXT_DIR"
echo ""
echo "Para ativar, execute (cada usuário precisa rodar isso):"
echo "  gnome-extensions enable $EXT_UUID"
echo ""
echo "Após ativar, reinicie a sessão GNOME (logout/login) para carregar a extensão."
