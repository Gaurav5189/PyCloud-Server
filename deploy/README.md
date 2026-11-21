# Deploying PyCloud behind Caddy

PyCloud serves HTTPS itself with an auto-generated self-signed certificate, which
is fine on a LAN but makes browsers warn and blocks anything that needs a trusted
certificate. Put Caddy in front and it will obtain a **real, trusted certificate**
automatically, plus HTTP→HTTPS redirects.

```
browser ──https──> Caddy (443, real cert) ──https──> PyCloud (127.0.0.1:8443, self-signed)
```

## 1. Bind PyCloud to localhost

Keep PyCloud's own TLS **on** — that keeps its session cookies `Secure` and makes
it emit HSTS. Only the bind address changes:

```bash
./pycloud -bind 127.0.0.1 -port 8443 -root /srv/pycloud/storage
```

Do not expose 8443 publicly once Caddy is in front; only 80 and 443 should be open.

## 2. Give it a hostname

Automatic TLS needs a DNS name that resolves to the server (certificates are never
issued for bare IP addresses). The quickest option needs no signup — `<ip>.nip.io`
resolves to that IP by turning the name into an address:

| Hostname | Resolves to |
|---|---|
| `203.0.113.10.nip.io` | `203.0.113.10` |
| `203.0.113.10.sslip.io` | `203.0.113.10` (alternative provider) |

For a name that survives an IP change, register a free one at
[DuckDNS](https://www.duckdns.org) or [FreeDNS](https://freedns.afraid.org) instead.
On EC2, an **Elastic IP** keeps the public IP (and therefore the nip.io name) stable.

## 3. Install Caddy

```bash
sudo ./install-caddy.sh 203.0.113.10.nip.io
```

> The Caddy binary is fetched from GitHub Releases. (Caddy's own
> `caddyserver.com/api/download` endpoint now requires an account and answers
> **401**, so it is not used.) To pin a version instead of resolving the latest:
> `CADDY_VERSION=2.11.9 sudo -E ./install-caddy.sh 203.0.113.10.nip.io`.

The script downloads the Caddy binary, writes `/etc/caddy/Caddyfile` and a
`caddy.service` systemd unit, and starts it. The domain is read from
`/etc/caddy/caddy.env`, so you can change it later without touching the Caddyfile:

```bash
sudo sed -i 's/^PYCLOUD_DOMAIN=.*/PYCLOUD_DOMAIN=new.example.com/' /etc/caddy/caddy.env
sudo systemctl restart caddy
```

## 4. Open the ports

- **Cloud firewall** (EC2 security group, etc.): allow inbound **TCP 80** and **TCP 443**.
  Port 80 is required for the ACME certificate challenge and for the redirect.
- **Host firewall** if enabled: `sudo ufw allow 80,443/tcp`

Then browse to `https://<your-hostname>`.

## Troubleshooting

| Symptom | Check |
|---|---|
| `journalctl -u caddy -f` shows an ACME failure | DNS must resolve to this box, and port 80 must be reachable from the internet. |
| 502 Bad Gateway | PyCloud isn't listening on `127.0.0.1:8443` — check `ss -ltnp \| grep 8443` and its logs. |
| Certificate not issued yet | Give it 30s, then `sudo systemctl restart caddy` and watch the journal. |
| Need to start over | `sudo rm -rf /var/lib/caddy/.local/share/caddy` and restart Caddy. |

## Using a real domain and a static certificate instead

If you own a domain pointing at the server, put it in `PYCLOUD_DOMAIN` and Caddy does
the rest. To terminate TLS elsewhere and let Caddy stream plain HTTP, run PyCloud with
`-no-tls` and change the proxy line to `reverse_proxy 127.0.0.1:8443`, dropping the
`transport http { ... }` block. Note this loses the `Secure` cookie flag and HSTS.
