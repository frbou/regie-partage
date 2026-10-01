#!/bin/sh
# Lance Régie Partage sur Mac (puce Apple ou Intel).
# Premier lancement : clic droit > Ouvrir (application non signée).
cd "$(dirname "$0")" || exit 1
xattr -dr com.apple.quarantine . 2>/dev/null
if [ "$(uname -m)" = "arm64" ]; then ./RegiePartage-apple; else ./RegiePartage-intel; fi
