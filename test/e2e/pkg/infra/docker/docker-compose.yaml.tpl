version: '2.4'

networks:
  {{ .Name }}:
    labels:
      e2e: true
    driver: bridge
{{- if .IPv6 }}
    enable_ipv6: true
{{- end }}
    ipam:
      driver: default
      config:
      - subnet: {{ .IP }}

services:
{{- range .Nodes }}
  {{ .Name }}:
    labels:
      e2e: true
    container_name: {{ .Name }}
    image: {{ .Version }}
{{- if eq .ABCIProtocol "builtin" }}
    entrypoint: /usr/bin/entrypoint-builtin
{{- else if .Misbehaviors }}
    entrypoint: /usr/bin/entrypoint-maverick
    command: ["node", "--misbehaviors", "{{ misbehaviorsToString .Misbehaviors }}"]
{{- end }}
    init: true
    ports:
    - 26656
    - {{ if .ProxyPort }}{{ .ProxyPort }}:{{ end }}26657
{{- if .PrometheusProxyPort }}
    - {{ .PrometheusProxyPort }}:26660
{{- end }}
    - 6060
    volumes:
    - ./{{ .Name }}:/cometbft
    - ./{{ .Name }}:/tendermint
    networks:
      {{ $.Name }}:
        ipv{{ if $.IPv6 }}6{{ else }}4{{ end}}_address: {{ .IP }}
{{- if ne .Version $.UpgradeVersion}}

  {{ .Name }}_u:
    labels:
      e2e: true
    container_name: {{ .Name }}_u
    image: {{ $.UpgradeVersion }}
{{- if eq .ABCIProtocol "builtin" }}
    entrypoint: /usr/bin/entrypoint-builtin
{{- else if .Misbehaviors }}
    entrypoint: /usr/bin/entrypoint-maverick
    command: ["node", "--misbehaviors", "{{ misbehaviorsToString .Misbehaviors }}"]
{{- end }}
    init: true
    ports:
    - 26656
    - {{ if .ProxyPort }}{{ .ProxyPort }}:{{ end }}26657
{{- if .PrometheusProxyPort }}
    - {{ .PrometheusProxyPort }}:26660
{{- end }}
    - 6060
    volumes:
    - ./{{ .Name }}:/cometbft
    - ./{{ .Name }}:/tendermint
    networks:
      {{ $.Name }}:
        ipv{{ if $.IPv6 }}6{{ else }}4{{ end}}_address: {{ .IP }}
{{- end }}

{{end}}