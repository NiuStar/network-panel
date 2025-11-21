#!/usr/bin/env bash
set -euo pipefail

echo "[install] fetching easytier install.sh from static host"
if ! wget -O /tmp/easytier.sh "https://panel-static.199028.xyz/network-panel/easytier/install.sh"; then
  echo "[install] static host unavailable, falling back to GitHub raw"
  wget -O /tmp/easytier.sh "https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install.sh"
fi
chmod +x /tmp/easytier.sh
sudo bash /tmp/easytier.sh uninstall || true
sudo rm -rf /opt/easytier
sudo bash /tmp/easytier.sh install
echo "[install] done"
