#!/usr/bin/env bash

# should be executed at the root directory of the project
set -eo pipefail

git clone https://github.com/celestiaorg/spec

cp -r spec/proto/tendermint proto/tendermint

buf generate --path proto/tendermint

find proto/tendermint/ -name "*.proto" -exec rm -rf {} \;

rm -rf spec
