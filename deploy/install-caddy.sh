#!/usr/bin/env bash
#
# Put PyCloud behind Caddy: real TLS certificate on 443, automatic HTTP->HTTPS.
#
# Usage:
#   sudo ./install-caddy.sh 203.0.113.10.nip.io
#   sudo ./install-caddy.sh                  # auto-detects the public IP -> <ip>.nip.io
#
# The domain can also come from the DOMAIN environment variable, but note that
# `sudo` strips variables unless it is invoked as `sudo -E` or with `env`.
#
# Expects PyCloud running on 127.0.0.1:8443 with its own TLS enabled, e.g.
#   ./pycloud -bind 127.0.0.1 -port 8443 -root /srv/pycloud/storage
#
set -euo pipefail

# --- 0. resolve the domain ---------------------------------------------------
PUBLIC_IP="${PUBLIC_IP:-$(curl -fsS --max-time 5 https://api.ipify.org || true)}"
DOMAIN="${1:-${DOMAIN:-${PUBLIC_IP:+$PUBLIC_IP.nip.io}}}"
if [ -z "${DOMAIN:-}" ]; then
  echo "Could not detect a public IP. Re-run with DOMAIN=<hostname>, e.g. DOMAIN=203.0.113.10.nip.io"
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "Run this with sudo." >&2
  exit 1
fi

# --- 1. install the Caddy static binary -------------------------------------
case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

if [ ! -x /usr/local/bin/caddy ]; then
  # caddyserver.com's download API now requires an account (it answers 401),
  # so pull the official prebuilt binary straight from GitHub Releases.
  CADDY_VERSION="${CADDY_VERSION:-$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    https://github.com/caddyserver/caddy/releases/latest | sed 's#.*/tag/v##')}"
  case "$CADDY_VERSION" in
    '' | */*)
      echo "Could not determine the latest Caddy version from GitHub." >&2
      echo "Set it explicitly and retry, e.g.:  CADDY_VERSION=2.11.9 sudo -E $0 $DOMAIN" >&2
      exit 1
      ;;
  esac

  TARBALL="caddy_${CADDY_VERSION}_linux_${ARCH}.tar.gz"
  echo "Downloading Caddy v${CADDY_VERSION} (${TARBALL}) ..."
  curl -fL --retry 3 \
    "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/${TARBALL}" \
    -o /tmp/caddy.tgz
  tar -xzf /tmp/caddy.tgz -C /tmp caddy
  install -m 0755 /tmp/caddy /usr/local/bin/caddy
  rm -f /tmp/caddy.tgz /tmp/caddy
fi

# --- 2. service account and directories -------------------------------------
NOLOGIN="$(command -v nologin || echo /usr/sbin/nologin)"
id -u caddy >/dev/null 2>&1 ||
  useradd --system --home-dir /var/lib/caddy --create-home --shell "$NOLOGIN" caddy
mkdir -p /etc/caddy /var/lib/caddy/.local/share
chown -R caddy:caddy /var/lib/caddy

# --- 3. Caddyfile + environment ---------------------------------------------
cat >/etc/caddy/caddy.env <<EOF
PYCLOUD_DOMAIN=$DOMAIN
EOF

cat >/etc/caddy/Caddyfile <<'EOF'
# The site address comes from PYCLOUD_DOMAIN (see /etc/caddy/caddy.env).
{$PYCLOUD_DOMAIN:localhost} {
	encode zstd gzip

	# PyCloud keeps its own self-signed TLS on localhost so that its session
	# cookies stay Secure and it emits HSTS; Caddy serves the trusted cert.
	reverse_proxy https://127.0.0.1:8443 {
		transport http {
			tls_insecure_skip_verify
		}
	}
}
EOF

# --- 4. systemd unit ---------------------------------------------------------
cat >/etc/systemd/system/caddy.service <<'EOF'
[Unit]
Description=Caddy reverse proxy for PyCloud
Documentation=https://caddyserver.com/docs/
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=caddy
Group=caddy
EnvironmentFile=/etc/caddy/caddy.env
ExecStart=/usr/local/bin/caddy run --environ --config /etc/caddy/Caddyfile --adapter caddyfile
ExecReload=/usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile --force
TimeoutStopSec=5s
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/var/lib/caddy
Environment=XDG_DATA_HOME=/var/lib/caddy/.local/share
Environment=XDG_CONFIG_HOME=/var/lib/caddy/.config

[Install]
WantedBy=multi-user.target
EOF

# --- 5. start ----------------------------------------------------------------
systemctl daemon-reload
systemctl enable --now caddy
systemctl --no-pager --lines=0 --quiet status caddy || true

echo
echo "  Caddy is proxying https://$DOMAIN  ->  https://127.0.0.1:8443"
echo "  First certificate issuance takes ~10-30s. Watch it with:"
echo "    journalctl -u caddy -f"
echo "  The security group must allow inbound TCP 80 and 443."
