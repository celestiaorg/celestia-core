FROM golang:1.25.1-alpine3.22

RUN apk update && \
    apk upgrade && \
    apk --no-cache add curl jq file

RUN go install github.com/go-delve/delve/cmd/dlv@latest

VOLUME /cometbft
WORKDIR /cometbft
EXPOSE 26656 26657
ENTRYPOINT ["/usr/bin/wrapper.sh"]
CMD ["node", "--proxy_app", "kvstore"]
STOPSIGNAL SIGTERM

COPY wrapper.sh /usr/bin/wrapper.sh
COPY config-template.toml /etc/cometbft/config-template.toml
