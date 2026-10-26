#!/bin/sh
# Builds the WebAssembly core of the web frontend into web/, next to the page.
# Then serve it with: mydiary-server serve -web web
set -eu
cd "$(dirname "$0")/.."
GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o web/mydiary.wasm ./cmd/mydiary-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/
gzip -9c web/mydiary.wasm >web/mydiary.wasm.gz
echo "Built web/mydiary.wasm"
