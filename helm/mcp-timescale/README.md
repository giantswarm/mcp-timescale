# mcp-timescale

Read-only MCP server for TimescaleDB and PostgreSQL databases, acting on the caller identity

**Homepage:** <https://github.com/giantswarm/mcp-timescale>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Giant Swarm |  | <https://giantswarm.io> |

## Source Code

* <https://github.com/giantswarm/mcp-timescale>

## Requirements

Kubernetes: `>=1.27.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| image.repository | string | `"gsoci.azurecr.io/giantswarm/mcp-timescale"` | Image repository. |
| image.tag | string | `""` | Image tag; defaults to .Chart.AppVersion. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| replicas | int | `1` | Replica count. One is enough with forwarded tokens (no OAuth state is shared); for more than one replica with interactive logins set storage.kind to valkey. |
| resources.requests.cpu | string | `"100m"` |  |
| resources.requests.memory | string | `"128Mi"` |  |
| resources.limits.cpu | string | `"500m"` |  |
| resources.limits.memory | string | `"512Mi"` |  |
| service.type | string | `"ClusterIP"` |  |
| service.port | int | `8080` |  |
| service.metricsPort | int | `9091` |  |
| ingress.enabled | bool | `false` |  |
| ingress.className | string | `""` |  |
| ingress.host | string | `""` |  |
| ingress.tls.enabled | bool | `false` |  |
| ingress.tls.secretName | string | `""` |  |
| databases | list | `[]` | Databases the server may connect to. Each entry becomes one database in databases.yaml; credentials come from a Secret mounted read-only (for a Zalando postgres-operator cluster that is the <role>.<cluster>.credentials.postgresql.acid.zalan.do Secret of a reader role). Optional keys: description, port (5432), sslmode (require), sslRootCertSecretRef {name, key} for verify-ca/verify-full, allowedGroups, allowedUsers (empty = every authenticated caller), maxRows (500, max 5000), statementTimeout (30s), maxConnections (4). |
| oauth.enabled | bool | `true` |  |
| oauth.provider | string | `"dex"` |  |
| oauth.issuerURL | string | `""` |  |
| oauth.audience | string | `"mcp-timescale"` |  |
| oauth.scopes[0] | string | `"openid"` |  |
| oauth.scopes[1] | string | `"email"` |  |
| oauth.scopes[2] | string | `"groups"` |  |
| oauth.allowInsecureHTTP | bool | `false` |  |
| oauth.allowLocalhostRedirectURIs | bool | `false` |  |
| oauth.allowPrivateURLs | bool | `false` | When true, the Dex issuer and its JWKS endpoint may resolve to private (RFC 1918), loopback or link-local addresses: a management cluster whose Dex sits behind an internal-only load balancer. Lifts mcp-oauth's SSRF guard for exactly these two operator-configured endpoints (nothing else may reach private addresses) and keeps TLS verification on. Without it every forwarded token is refused there because the JWKS fetch fails. Rendered as OAUTH_ALLOW_PRIVATE_URLS; the same knob as allowPrivateURLs on mcp-kubernetes, mcp-capi and mcp-prometheus. |
| oauth.dex.issuerURL | string | `""` |  |
| oauth.dex.clientIDSecretRef.name | string | `""` |  |
| oauth.dex.clientIDSecretRef.key | string | `"client-id"` |  |
| oauth.dex.clientSecretSecretRef.name | string | `""` |  |
| oauth.dex.clientSecretSecretRef.key | string | `"client-secret"` |  |
| oauth.trustedAudiences | list | `[]` | Trusted audiences for SSO token forwarding (RFC 8693): the Dex client IDs whose ID tokens muster forwards (auth.mode forward). Each entry must match [a-zA-Z0-9_-]{1,256}. |
| oauth.trustedRedirectSchemes | list | `[]` |  |
| oauth.encryptionKeySecretRef.name | string | `""` |  |
| oauth.encryptionKeySecretRef.key | string | `"encryption-key"` |  |
| oauth.sessionIDHMACKeySecretRef.name | string | `""` |  |
| oauth.sessionIDHMACKeySecretRef.key | string | `"session-id-hmac-key"` |  |
| storage.kind | string | `"memory"` | OAuth state store. memory: per-pod, lost on restart, not shared across replicas — sufficient when clients arrive with forwarded tokens (muster). valkey: required for replicas > 1 with interactive logins. |
| storage.valkey.address | string | `""` |  |
| storage.valkey.passwordSecretRef.name | string | `""` |  |
| storage.valkey.passwordSecretRef.key | string | `"password"` |  |
| storage.valkey.tls | bool | `true` |  |
| gatewayAPI.enabled | bool | `false` |  |
| gatewayAPI.httpRoute.parentRefs | list | `[]` |  |
| gatewayAPI.httpRoute.hostnames | list | `[]` |  |
| gatewayAPI.httpRoute.labels | object | `{}` |  |
| gatewayAPI.httpRoute.annotations | object | `{}` |  |
| gatewayAPI.backendTrafficPolicy.enabled | bool | `false` |  |
| gatewayAPI.backendTrafficPolicy.timeout | string | `"0s"` |  |
| gatewayAPI.backendTrafficPolicy.labels | object | `{}` |  |
| gatewayAPI.backendTrafficPolicy.annotations | object | `{}` |  |
| serviceMonitor.enabled | bool | `true` |  |
| serviceMonitor.interval | string | `"30s"` |  |
| serviceMonitor.scrapeTimeout | string | `"10s"` |  |
| serviceMonitor.labels | object | `{}` |  |
| networkPolicy | object | `{"egressAllowlist":[],"enabled":false}` | NetworkPolicy with default-deny egress (DNS + egressAllowlist only). Off by default because the databases and Dex are reached by DNS name and a NetworkPolicy cannot express that; when enabling it, allow TCP 5432 to the database namespaces and TCP 443 for Dex JWKS. Example:   egressAllowlist:     - to:         - namespaceSelector:             matchLabels: {kubernetes.io/metadata.name: timescale-demo}       ports:         - protocol: TCP           port: 5432     - to:         - ipBlock: {cidr: 0.0.0.0/0}       ports:         - protocol: TCP           port: 443 |
| podDisruptionBudget | object | `{"enabled":false,"minAvailable":1}` | PodDisruptionBudget. Off by default because replicas is 1 (minAvailable 1 would block node drains); enable together with replicas >= 2. |
| autoscaling.enabled | bool | `false` |  |
| autoscaling.minReplicas | int | `2` |  |
| autoscaling.maxReplicas | int | `6` |  |
| autoscaling.targetCPUUtilizationPercentage | int | `70` |  |
| otel.endpoint | string | `""` |  |
| otel.protocol | string | `"http/protobuf"` |  |
| extraEnv | list | `[]` | Any additional non-secret env vars. |
| extraEnvFromSecretRefs | list | `[]` | Any Secret refs to mount as env vars ({envName, name, key}). |
| podSecurityContext.runAsNonRoot | bool | `true` |  |
| podSecurityContext.runAsUser | int | `65534` |  |
| podSecurityContext.runAsGroup | int | `65534` |  |
| podSecurityContext.fsGroup | int | `65534` |  |
| podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| securityContext.runAsNonRoot | bool | `true` |  |
| securityContext.runAsUser | int | `65534` |  |
| securityContext.runAsGroup | int | `65534` |  |
| securityContext.readOnlyRootFilesystem | bool | `true` |  |
| securityContext.allowPrivilegeEscalation | bool | `false` |  |
| securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| serviceAccount.create | bool | `true` |  |
| serviceAccount.name | string | `""` |  |
| serviceAccount.annotations | object | `{}` |  |
| serviceAccount.automountServiceAccountToken | bool | `false` | The server never talks to the Kubernetes API; no token is mounted. |
| nodeSelector | object | `{}` |  |
| tolerations | list | `[]` |  |
| affinity | object | `{}` |  |
| topologySpreadConstraints | list | `[]` |  |
| nameOverride | string | `""` |  |
| fullnameOverride | string | `""` |  |
