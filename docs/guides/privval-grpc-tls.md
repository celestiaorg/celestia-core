# Securing the PrivValidator gRPC endpoint with mutual TLS

The node can expose its private validator over gRPC (`priv_validator_grpc_laddr`)
so an external service, such as the fibre server, can request signatures. The
endpoint signs raw bytes with the validator consensus key, so anyone who can
reach it can request unauthorized signatures.

The node therefore refuses to start when `priv_validator_grpc_laddr` is set to a
non-loopback address unless mutual TLS is fully configured (or the check is
explicitly bypassed with `priv_validator_grpc_allow_insecure = true`). Only
loopback IP literals (`127.0.0.1`, `::1`) count as loopback; hostnames,
including `localhost`, are treated as exposed because the resolver may map them
elsewhere.

## Generating certificates

Use a dedicated certificate authority (CA) for this endpoint. The example below
uses `openssl` and issues one server certificate for the node and one client
certificate for the signer client (e.g. the fibre server).

```sh
# 1. Create the CA.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout ca.key -out ca.crt -days 3650 -subj "/CN=privval-ca"

# 2. Server certificate. The SAN must match the address clients dial.
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout server.key -out server.csr -subj "/CN=privval-server"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 825 \
  -extfile <(printf "subjectAltName=IP:10.0.0.5\nextendedKeyUsage=serverAuth")

# 3. Client certificate for the signer client.
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout client.key -out client.csr -subj "/CN=privval-client"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 825 \
  -extfile <(printf "extendedKeyUsage=clientAuth")
```

Replace `IP:10.0.0.5` with the IP or DNS name (`DNS:signer.example.com`) the
client uses to reach the node. Keep `ca.key` offline; it is only needed to issue
new certificates.

## Node configuration

In `config.toml`:

```toml
priv_validator_grpc_laddr = "10.0.0.5:26669"
priv_validator_grpc_cert_file = "server.crt"
priv_validator_grpc_key_file = "server.key"
priv_validator_grpc_client_ca_file = "ca.crt"
```

Paths are absolute or relative to the node home. All three must be set
together; leave all three empty for plaintext on a loopback IP. Only clients
presenting a certificate signed by the CA may request signatures.

## Client configuration (fibre)

In the fibre `server_config.toml`, point the signer client at the same CA and
give it the client certificate:

```toml
signer_grpc_address = "10.0.0.5:26669"
signer_grpc_ca_file = "ca.crt"
signer_grpc_cert_file = "client.crt"
signer_grpc_key_file = "client.key"
```

## Certificate rotation

Certificates are loaded at startup. To rotate, issue new certificates from the
same CA and restart the node and the client.
