#!/bin/sh

go run github.com/vektra/mockery/v2@latest --disable-version-string --case underscore --name $*
