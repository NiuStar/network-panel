#!/usr/bin/env bash
set -euo pipefail

echo "[install] fetching easytier install.sh from static host"
if ! wget -T 10  --tries=1 -O /tmp/easytier.sh "https://panel-static.199028.xyz/network-panel/easytier/install_easytier.sh"; then
  echo "[install] static host unavailable, falling back to GitHub raw"
  wget -T 10 -O /tmp/easytier.sh "https://proxy.529851.xyz/https://raw.githubusercontent.com/EasyTier/EasyTier/main/script/install.sh"
fi
chmod +x /tmp/easytier.sh
sudo bash /tmp/easytier.sh uninstall || true
sudo rm -rf /opt/easytier
sudo bash /tmp/easytier.sh install
echo "[install] done"
