#!/bin/bash
# watashi masque v1.0.0-masque - builds the MASQUE (HTTP/3) engine from
# upstream usque v4.2.1 plus the watashi headless subcommand.
#
# usage (root, on any Linux box with internet):
#   bash build-masque.sh              build and leave the zips in ./dist
#   bash build-masque.sh --install    the same, then put the binary for this
#                                     machine into the panel and restart WARP
#
# Nothing is downloaded from a mirror: the source comes from the upstream tag,
# Go from go.dev, modules from the Go module proxy, all over https.
set -euo pipefail
cd "$(dirname -- "$0")"
KIT=$(pwd)

UPSTREAM_TAG=${UPSTREAM_TAG:-v4.2.1}
WS_VERSION=${WS_VERSION:-v1.0.0-masque}
GO_VERSION=${WS_GO_VERSION:-go1.26.8}
WORK=${WS_WORK:-/opt/ws-build/watashi-masque}
GOROOT_DIR=${WS_GOROOT:-/opt/ws-build/go}
DIST="$KIT/dist"
INSTALL=0
[ "${1:-}" = "--install" ] && INSTALL=1

say() { printf '\n== %s\n' "$*"; }
die() { printf '\n!! %s\n' "$*" >&2; exit 1; }

host_arch() {
    case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) die "unsupported machine: $(uname -m)" ;;
    esac
}

# ------------------------------------------------------------------ Go
say "Go $GO_VERSION"
if [ ! -x "$GOROOT_DIR/bin/go" ] || [ "$("$GOROOT_DIR/bin/go" env GOVERSION 2>/dev/null)" != "$GO_VERSION" ]; then
    rm -rf "$GOROOT_DIR"
    mkdir -p "$(dirname "$GOROOT_DIR")"
    tgz="/tmp/$GO_VERSION.linux-$(host_arch).tar.gz"
    curl -fL --retry 3 -o "$tgz" "https://go.dev/dl/$GO_VERSION.linux-$(host_arch).tar.gz"
    tar -C "$(dirname "$GOROOT_DIR")" -xzf "$tgz"
    [ "$(dirname "$GOROOT_DIR")/go" = "$GOROOT_DIR" ] || mv "$(dirname "$GOROOT_DIR")/go" "$GOROOT_DIR"
    rm -f "$tgz"
fi
export PATH="$GOROOT_DIR/bin:$PATH" GOTOOLCHAIN=local GOFLAGS=-mod=mod
go version

# ------------------------------------------------------------------ source
say "upstream usque $UPSTREAM_TAG"
rm -rf "$WORK"
mkdir -p "$WORK"
curl -fL --retry 3 "https://github.com/Diniboy1123/usque/archive/refs/tags/$UPSTREAM_TAG.tar.gz" |
    tar -xz -C "$WORK" --strip-components=1
cd "$WORK"

# the watashi subcommand was written against this exact base; anything else
# means the base moved and it must be looked at again before it is used
grep -q '^module github.com/Diniboy1123/usque$' go.mod || die "go.mod is not the $UPSTREAM_TAG the kit expects"
grep -q '"github.com/Diniboy1123/usque/config"' cmd/root.go || die "cmd/root.go is not the $UPSTREAM_TAG the kit expects"

say "applying the watashi headless subcommand"
cp -f "$KIT/masque/cmd/watashi.go" cmd/watashi.go
gofmt -w cmd/watashi.go

say "checks"
go mod download
go vet ./cmd/...

# ------------------------------------------------------------------ build
say "building $WS_VERSION"
rm -rf "$DIST"
mkdir -p "$DIST"
for arch in amd64 arm64; do
    out="$WORK/out/$arch"
    mkdir -p "$out"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X github.com/Diniboy1123/usque/cmd.version=$WS_VERSION" -o "$out/watashi-masque" .
    # python instead of zip: not every server has the zip command
    python3 - "$out/watashi-masque" "$DIST/watashi-masque_linux-$arch.zip" <<'PY'
import sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(dst, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    info = zipfile.ZipInfo('watashi-masque')
    info.external_attr = 0o100755 << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    z.writestr(info, open(src, 'rb').read())
PY
done
cd "$DIST"
sha256sum watashi-masque_linux-*.zip | tee SHA256SUMS

say "lines for common/packages.lock once the zips are published"
echo "replace OWNER/REPO with your repository:"
while read -r sum file; do
    arch=${file#watashi-masque_linux-}
    arch=${arch%.zip}
    echo "watashi-masque|${WS_VERSION#v}|$arch|https://github.com/OWNER/REPO/releases/download/$WS_VERSION/$file|$sum"
done <SHA256SUMS

# ------------------------------------------------------------------ install
if [ "$INSTALL" = 1 ]; then
    say "installing into the panel"
    dst=/opt/watashi-manager/other/warp/warpplus
    [ -d "$dst" ] || die "$dst does not exist"
    arch=$(host_arch)
    [ -f "$dst/watashi-masque" ] && cp -f "$dst/watashi-masque" "$dst/watashi-masque.previous"
    install -m 755 "$WORK/out/$arch/watashi-masque" "$dst/watashi-masque"
    bash "$dst/run.sh" || true
    echo "to go back: install -m 755 $dst/watashi-masque.previous $dst/watashi-masque && systemctl restart watashi-warp"
fi
