# Running LP-500 / LP-700 server behind Caddy (TLS + login)

`lp700-server` has no login of its own. Bound to the LAN, anything that can reach the
port can read it and issue control commands. Since VU3ESV/LP-700-Server#7, `/ws` refuses
browsers from a *different* origin, but any native client or script on the
LAN still gets in, in clear text.

This guide puts [Caddy](https://caddyserver.com/) in front of it, on the same
port number:

```
LAN clients  ──https/wss──►  Caddy  <lan-ip>:8089   TLS + login + Origin check
                               │
                               ▼  reverse_proxy (WebSocket included)
local clients ──http/ws──►  lp700-server  127.0.0.1:8089   unchanged
```

Node-RED (or anything else) on the same host keeps using `127.0.0.1:8089` with no login. LAN clients switch from
`http://`/`ws://` to `https://`/`wss://` and send a login. Endpoints:
`/` (web client), `/ws` (telemetry, scope/spectrum, control), `/healthz`, `/api/config`, `/api/log-level`, `/api/update`.

Checked against Caddy 2.6.2 (the Debian 13 package) on a Raspberry Pi.

## 1. Bind the server to loopback

In `/etc/lp700-server/config.toml`:

```toml
[server]
listen = "127.0.0.1:8089"   # was "0.0.0.0:8089"
```

```sh
sudo systemctl restart lp700-server
ss -ltn '( sport = :8089 )'          # expect 127.0.0.1:8089 only
```

`deploy/redeploy.sh` without `--keep-config` replaces `config.toml` with the
template (it preserves only the USB IDs and `updates.check`); use
`--keep-config` (binary only) once you have changed `listen`.

Caddy can then own `<lan-ip>:8089` while the server keeps `127.0.0.1:8089`.
Linux allows two sockets on the same port when their addresses differ, as
long as neither uses `0.0.0.0`.

Point local clients at **`127.0.0.1`, not `localhost`**. With the server on
IPv4 loopback only, `localhost` may resolve to `::1` first.

## 2. A certificate the clients trust

macOS and iOS (Safari, `URLSession`, the Mac apps) accept a TLS server
certificate only if:

- **every name and address clients use is in `subjectAltName`**: IP SANs for
  LAN IPs and DNS SANs for `host.lan` / `host.local`. The CN is ignored;
- it has **`extendedKeyUsage = serverAuth`**;
- it is valid for **at most 825 days**. This applies to private CAs too;
- the key is RSA ≥ 2048 or EC P-256.

With a private CA, make it **name-constrained** so that a leaked CA key can't
impersonate public sites to the Macs that trust it (OpenSSL 3):

```sh
# CA (once). Keep ca.key off the server.
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -aes-256-cbc -out ca.key
cat > ca.cnf <<'EOF'
[req]
distinguished_name = dn
prompt             = no
x509_extensions    = v3_ca
[dn]
CN = My Shack CA
[v3_ca]
basicConstraints     = critical,CA:TRUE,pathlen:0
keyUsage             = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
nameConstraints      = critical,permitted;IP:192.168.1.0/255.255.255.0,permitted;DNS:lan,permitted;DNS:local
EOF
openssl req -x509 -new -key ca.key -days 3650 -sha256 -config ca.cnf -out ca.crt

# Server cert (renew before day 825)
openssl ecparam -name prime256v1 -genkey -noout -out server.key
openssl req -new -key server.key -subj "/CN=mypi.lan" -out server.csr
cat > server.ext <<'EOF'
basicConstraints       = critical,CA:FALSE
keyUsage               = critical,digitalSignature
extendedKeyUsage       = serverAuth
subjectAltName         = IP:192.168.1.50,DNS:mypi.lan,DNS:mypi.local
authorityKeyIdentifier = keyid
EOF
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 800 -sha256 -extfile server.ext -out server.crt
openssl verify -CAfile ca.crt server.crt          # server.crt: OK
```

Trust the CA on each Mac. macOS asks for your login password:

```sh
security add-trusted-cert -r trustRoot -p ssl -k ~/Library/Keychains/login.keychain-db ca.crt
```

On the server:

```sh
sudo install -d -o root -g caddy -m 750 /etc/caddy/tls
sudo install -o root -g caddy -m 644 server.crt /etc/caddy/tls/server.crt
sudo install -o root -g caddy -m 640 server.key /etc/caddy/tls/server.key
```

## 3. A login

Caddy stores only a bcrypt hash. Pass the password on stdin so it never
appears on a command line:

```sh
read -rs PASS && printf '%s
' "$PASS" | caddy hash-password; unset PASS
```

- **End the input with a newline.** `caddy hash-password` reads one line, and
  without the newline it fails with `Error: EOF`. The newline is not part of
  the hash.
- **The output is the raw `$2a$14$…` hash**, which goes straight into the
  Caddyfile. Caddy 2.6.2 also accepts the hash base64-encoded.
- **Alternative:** `printf '%s' "$PASS" | htpasswd -niBC 12 operator | cut -d: -f2-`
  produces an equivalent `$2y$12$…` hash (`htpasswd` ships with macOS;
  `apache2-utils` on Debian).

On the Mac, the VU3ESV apps read the login from a Keychain item they share:
generic password, **service `ShackProxy`**, **account = username**. Create it
from the app's *Proxy login* section, or:

```sh
security add-generic-password -U -s ShackProxy -a operator -l "Shack proxy login" -w   # prompts
```

## 4. Caddyfile

```caddyfile
{
	auto_https off
}

https://:8089 {
	bind <lan-ip>
	tls /etc/caddy/tls/server.crt /etc/caddy/tls/server.key

	# Browsers send Origin on WebSocket upgrades and POSTs: refuse any other site.
	@foreign expression `{http.request.header.Origin} != "" && {http.request.header.Origin} != "https://" + {http.request.hostport}`
	respond @foreign "cross-origin request refused" 403

	basicauth bcrypt "LP-700" {          # Caddy >= 2.8 also spells it basic_auth
		operator <BCRYPT_HASH>
	}

	header -Server
	reverse_proxy 127.0.0.1:8089
}
```

- **`auto_https off`:** the certificate is your own, so there is no ACME and no port-80 redirect listener.
- **`bind <lan-ip>`:** list every LAN address (`bind 192.168.1.50 192.168.1.51`). If DHCP may assign them after Caddy starts, set `net.ipv4.ip_nonlocal_bind = 1` in `/etc/sysctl.d/`, or Caddy fails to start with *cannot assign requested address*.
- **Keep the original `Host`:** `reverse_proxy` passes it through by default, so don't add `header_up Host …`. The server's own same-origin check compares the browser's `Origin` with `Host`, and a rewritten `Host` makes it refuse the proxied web UI.
- **Order:** Caddy runs `basicauth` before `respond`. A foreign origin without a login gets 401; with a login, 403.
- **WebSocket:** upgrades need nothing extra. A browser that has logged in to the page sends the same Basic credentials on the `/ws` upgrade.

Install Caddy **without** it starting the package's default `:80` site, then
add the config:

```sh
printf '#!/bin/sh\nexit 101\n' | sudo tee /usr/sbin/policy-rc.d >/dev/null && sudo chmod 755 /usr/sbin/policy-rc.d
sudo apt-get install -y caddy
sudo rm -f /usr/sbin/policy-rc.d
sudoedit /etc/caddy/Caddyfile
sudo -u caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile   # Valid configuration
sudo systemctl enable --now caddy      # later changes: sudo systemctl reload caddy
```

To try it first without touching the real port, serve it on a spare port
(e.g. `https://:9089`) that proxies to the still-public server, test
the clients there, then switch both at once.

## 5. Verify

From a client that trusts the CA:

```sh
CA=ca.crt; U=https://<lan-ip>:8089
curl -s -o /dev/null -w '%{http_code}\n' --cacert $CA $U/healthz                      # 401
curl -s -o /dev/null -w '%{http_code}\n' --cacert $CA -u operator $U/healthz          # 200 (prompts)
curl -s -o /dev/null -w '%{http_code}\n' --cacert $CA -u operator \
     -H 'Origin: https://evil.example' $U/                                                # 403
curl -s -i -N -m 3 --http1.1 --cacert $CA -u operator -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
     -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' $U/ws | head -1   # 101
openssl s_client -connect <lan-ip>:8089 -CAfile $CA </dev/null 2>/dev/null | grep 'Verify return code'  # 0 (ok)
curl -s -o /dev/null -w '%{http_code}\n' http://<lan-ip>:8089/          # 400: plain HTTP to the TLS port
```

On the server, `ss -ltn '( sport = :8089 )'` should show `127.0.0.1:8089`
for the server and `<lan-ip>:8089` for Caddy.

## 6. Clients

**LP-700-App** (v0.1.19 or later):

- **Server URL:** `https://<lan-ip>:8089`.
- **Proxy login** in the connection sheet: username `operator`, password, **Save to Keychain**.
- Builds from LP-700-App#26 onwards drop a pasted trailing newline from the password.
- The app sends the login only over `https`/`wss`.

**Browser:** open `https://<lan-ip>:8089/` and log in when prompted.

**Shared login:** the apps share the `ShackProxy` item. If another app already
saved it, there's nothing to type. Saving over it changes it for every app.

**Copying the password:** `security find-generic-password -s ShackProxy -a operator -w`
prints the password **plus a trailing newline**, so add `| tr -d '\n' | pbcopy`
when you copy it.

## Troubleshooting

| Symptom | Cause |
|---|---|
| 401 although the password is right | The stored password has a newline in it, from pasting `security … -w` output. If `security … -w` prints **hex**, the item holds non-printable bytes. Save the login again, pasting just the password. |
| 403 in the browser | `Host` was rewritten (see §4), or the page comes from another origin, such as a dashboard on another port. |
| TLS error in the app or Safari | The CA isn't trusted on that Mac, the address isn't in the SAN, or the certificate is valid for more than 825 days. |
| Caddy won't start: *address already in use* | The server still listens on `0.0.0.0:8089`; do step 1 first. |
| Caddy won't start: *cannot assign requested address* | The bind address isn't on an interface yet; see `ip_nonlocal_bind` in §4. |
| `caddy hash-password` fails with `Error: EOF` | It reads one line: end the input with a newline (`printf '%s
'`). |
