#!/bin/sh
# Compile Régie Partage pour Windows et prépare le zip à livrer.
#   VERSION=1.0.0 ./build.sh   →  dist/RegiePartage-1.0.0-windows.zip
set -e
cd "$(dirname "$0")"
VERSION="${VERSION:-1.1.0}"

go vet ./...
go test ./...

rm -rf dist && mkdir -p "dist/RegiePartage"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "dist/RegiePartage/RegiePartage.exe" .
cp docs/LISEZMOI.txt docs/GUIDE-ADMINISTRATEUR.txt "dist/RegiePartage/"
(cd dist && zip -qr "RegiePartage-$VERSION-windows.zip" RegiePartage)

# Mac (essais) : binaires puce Apple et Intel + lanceur à double-cliquer.
M="dist/mac/RegiePartage"
mkdir -p "$M"
for a in arm64:apple amd64:intel; do
  GOOS=darwin GOARCH="${a%%:*}" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$M/RegiePartage-${a##*:}" .
done
cp "mac/Lancer Régie Partage.command" docs/LISEZMOI.txt docs/GUIDE-ADMINISTRATEUR.txt "$M/"
(cd dist/mac && zip -qry "../RegiePartage-$VERSION-mac.zip" RegiePartage)
echo "dist/RegiePartage-$VERSION-windows.zip dist/RegiePartage-$VERSION-mac.zip"
