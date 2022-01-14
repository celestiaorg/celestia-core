#!/usr/bin/env bash

# should be executed at the root directory of the project
set -eo pipefail

git clone https://github.com/celestiaorg/spec

cp -r spec/proto/tendermint proto

buf generate --path proto/tendermint

mv ./proto/tendermint/abci/types.pb.go ./abci/types

find proto/tendermint/ -name "*.proto" -exec rm -rf {} \;

rm -rf spec
