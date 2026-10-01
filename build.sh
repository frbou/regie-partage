#!/bin/sh
# Compile Régie Partage pour Windows et prépare le zip à livrer.
#   VERSION=1.0.0 ./build.sh   →  dist/RegiePartage-1.0.0-windows.zip
set -e
cd "$(dirname "$0")"
VERSION="${VERSION:-1.0.0}"

go vet ./...
go test ./...

rm -rf dist && mkdir -p "dist/RegiePartage"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "dist/RegiePartage/RegiePartage.exe" .
cp docs/LISEZMOI.txt docs/GUIDE-ADMINISTRATEUR.txt "dist/RegiePartage/"
(cd dist && zip -qr "RegiePartage-$VERSION-windows.zip" RegiePartage)
echo "dist/RegiePartage-$VERSION-windows.zip"
