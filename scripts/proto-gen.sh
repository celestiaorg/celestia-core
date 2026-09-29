#!/bin/sh
#
# Update the generated code for protocol buffers in the CometBFT repository.
# This must be run from inside a CometBFT working directory.
#
set -euo pipefail

# Work from the root of the repository.
cd "$(git rev-parse --show-toplevel)"

# Run inside Docker to install the correct versions of the required tools
# without polluting the local system.
docker run --rm -i -v "$PWD":/w --workdir=/w golang:1.26-alpine sh <<"EOF"
apk add git make

# No buf install: make proto-gen runs buf via `go run` at the version pinned in the Makefile.
go install github.com/cosmos/gogoproto/protoc-gen-gogofaster@v1.7.2
make proto-gen
EOF
