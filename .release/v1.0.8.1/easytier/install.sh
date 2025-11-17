#!/usr/bin/env bash
set -euo pipefail

echo "[install] fetching easytier install.sh from GitHub"
wget -O /tmp/easytier.sh "https://static-sg.529851.xyz/easytier/install.sh"
chmod +x /tmp/easytier.sh
sudo bash /tmp/easytier.sh uninstall || true
sudo rm -rf /opt/easytier
sudo bash /tmp/easytier.sh install --gh-proxy http://proxy.529851.xyz/
echo "[install] done"
