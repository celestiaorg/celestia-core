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
docker run --rm -i -v "$PWD":/w --workdir=/w golang:1.24-alpine sh <<"EOF"
set -e
apk add git make

go install github.com/bufbuild/buf/cmd/buf@latest
go install github.com/cosmos/gogoproto/protoc-gen-gogofaster@latest

# Add Go bin to PATH so buf can be found
export PATH="$PATH:/root/go/bin"

# Generate protobuf files
echo "Generating Protobuf files"
buf generate


# Move generated files to their final locations
# (paths are relative to /w which is the repo root mounted by Docker)
mv ./proto/tendermint/abci/types.pb.go ./abci/types/
cp ./proto/tendermint/rpc/grpc/types.pb.go ./rpc/grpc
EOF
