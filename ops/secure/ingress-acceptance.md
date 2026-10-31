# Dedicated payment ingress acceptance

Use [nginx.conf.example](nginx.conf.example) for only the two dedicated hosts. The customer payment page remains on the merchant site. Do not replace the existing merchant virtual hosts or reload Nginx until the complete installed configuration passes `nginx -t`.

## Prepare and inspect

1. Verify that both DNS records are proxied through Cloudflare and that the effective origin TLS mode is Full (strict). Check existing rules/overrides as well as the zone default. An orange-cloud DNS answer alone does not prove the TLS mode or cache policy.
2. Check the origin certificate's SAN, validity, and private-key path/permissions. The server certificate authenticates the origin to Cloudflare; it does not stop direct-origin access.
3. Download the **public** AOP CA from [Cloudflare](https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem), inspect its validity/fingerprint, and install it at the reviewed path during deployment. Do not use the Origin CA server certificate as the AOP client CA.
4. Enable and verify Authenticated Origin Pulls on the Cloudflare side before requiring its certificate in the new Nginx virtual hosts. The new template uses `ssl_verify_client on`. A missing CA must fail configuration validation; absent/untrusted client certificates must never reach the application.
5. Cloudflare's shared AOP certificate identifies Cloudflare infrastructure, **not your account or an operator**. This profile has no management hostname: service API requests still require both signatures (business HMAC + service MAC), and operator access is an SSH tunnel to the loopback listener. Do not treat AOP as application authentication.
6. Hostname-level AOP/custom certificates may require a different plan. If using zone-level AOP, review all origin destinations for that zone first. Enforce client certificates only on the new payment hosts; do not change unrelated virtual hosts or zone TLS defaults without reviewing their impact.

For delegated inspection/configuration, a token restricted to the target zone may need `Zone → Zone → Read`, `Zone → DNS → Read`, `Zone → Zone Settings → Read`, and `Zone → SSL and Certificates → Edit`. The existing account-scoped Access permission does not grant these automatically. Keep account Access permissions only while still needed; no billing, Global API Key, or DNS editing is required for these checks/AOP. Read and preserve current state before any authorized update; verify by GET afterward.

## Template behavior

- HTTP for either exact hostname returns 404 on IPv4 and IPv6. It cannot fall through to the host's default HTTP site.
- HTTPS declares both address families, rejects mismatched Host/SNI, requires the AOP client certificate, disables inherited proxy caching and adds `Cache-Control: no-store`.
- Only the four enumerated POST service routes are forwarded on the service host. Administration and legacy routes return 404 there.
- Only `/admin` and its descendants reach the administration application. The Access assertion is forwarded intact; Nginx does not itself validate the JWT. The root redirects to `/admin`.
- Rate limiting returns 429. The upstream remains a loopback-only Docker port.

TLS protocol negotiation can also depend on Nginx's default TLS server on a shared listener. Declaring TLS 1.2/1.3 in a named server is not proof of the complete host's handshake policy. Check real handshakes after installation; review any necessary default-server change separately from this template.

## Isolated regression

On an isolated host/CI runner with Python 3, Nginx and OpenSSL:

```sh
EPUSDT_TEST_NGINX=1 python3 -B -m unittest discover -s ops/secure -p test_nginx_ingress.py -v
```

The test uses synthetic certificates, an in-process fake upstream, random loopback ports, a private temporary directory, and a separate Nginx process. It never loads the production Nginx configuration or reloads the system service. It checks HTTP rejection, missing client certificate, POST allowlist, legacy/operator route rejection (including `/status`), Host/SNI mismatch, cache headers and IPv6. IPv6 is explicitly skipped if the test host lacks it; opted-in execution with missing Nginx/OpenSSL fails instead of silently skipping. CI enables the opt-in.

This checks the template boundary, not Cloudflare, AOP enforcement, global origin routing, rate-limit capacity, or payment settlement. Ordinary local Python test discovery skips this suite unless explicitly enabled.

## Production acceptance, with payments still disabled

After deploying the reviewed image/configuration, verify the full Nginx configuration and then perform a controlled reload. Record all of the following separately:

1. Valid signed service request succeeds via Cloudflare; old gateway routes, `/status`, `/healthz` and any administration path are absent on that hostname.
2. The SSH-tunnelled `curl /status` returns scan health, and the same path is 404 through the public hostname.
3. Direct-origin IPv4/IPv6, missing client certificate, alternative Host and SNI combinations, HTTP and unrelated virtual hosts cannot reach a protected route. Use `--resolve`/equivalent to preserve the intended TLS hostname; a test that fails only because the server certificate is untrusted does not prove AOP enforcement.
4. The published gateway port is loopback-only, and service responses are not cached by Cloudflare or Nginx.
5. Existing merchant/Alipay health remains normal. Record the deployed config hash, image ID/digest, actual test time and remaining gaps.

Do not mark these production checks passed from an isolated fake-upstream run. Payment switches and the SQL freeze are released only through the separate payment acceptance process in [the runbook](README.md).
