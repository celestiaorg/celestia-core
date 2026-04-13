# We use Debian instead of Alpine for compatibility.
FROM golang:1.26.2

RUN apt-get -qq update -y && apt-get -qq upgrade -y >/dev/null

# Set up build directory /src/cometbft
WORKDIR /src/cometbft

# Fetch dependencies separately (for layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Build CometBFT and install into /usr/bin/cometbft
COPY . .
RUN make build && cp build/cometbft /usr/bin/cometbft
COPY test/e2e/docker/entrypoint* /usr/bin/
RUN cd test/e2e && make node && cp build/node /usr/bin/app

# Set up runtime directory. We don't use a separate runtime image since we need
WORKDIR /cometbft
VOLUME /cometbft
ENV CMTHOME=/cometbft
ENV GORACE="halt_on_error=1"

EXPOSE 26656 26657 26660 6060
ENTRYPOINT ["/usr/bin/entrypoint"]
CMD ["node"]
STOPSIGNAL SIGTERM
