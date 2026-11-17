# Private-address JWKS settings migration

Use `OAUTH_ALLOW_PRIVATE_IP_JWKS=true` or Helm `oauth.allowPrivateIPJWKS: true`
only when the configured JWKS endpoint must resolve to private addresses.
HTTPS and certificate verification remain required. Dex discovery and token requests
already support private addresses and honor `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`.

Remove `OAUTH_ALLOW_PRIVATE_URLS`, its `MCP_OAUTH_ALLOW_PRIVATE_URLS` alias, and Helm
`oauth.allowPrivateURLs`. An enabled obsolete setting fails startup or chart rendering
with a migration message, including when the replacement setting is false.
Obsolete false values remain accepted temporarily and do not change the JWKS policy;
both obsolete environment spellings are validated even when another setting is supplied.

Helm `oauth.allowLocalhostRedirectURIs` is deprecated and ignored. Native-app loopback
redirect URIs are always accepted on any port, as required by RFC 8252 section 7.3.
Other redirect URIs require HTTPS unless an explicit redirect policy permits HTTP.

Removal of these compatibility settings is planned for v0.9.0 or 2027-02-17,
whichever comes first. Until then, the Helm schema restricts `oauth.allowPrivateURLs`
to false and neither obsolete environment variable is rendered.
