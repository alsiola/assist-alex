#!/bin/sh
# Installs Assist into the local Open Conduit: the extension package, the
# assistant's home, the `assist` scope, and the Mac app. Safe to run again:
# it updates what is there.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
oc=${OPEN_CONDUIT:-open-conduit}
state=${OPEN_CONDUIT_STATE_DIR:-"$HOME/.local/state/open-conduit"}

api() { # GET /v1/$1 as the operator, printing the data field's key $2
	python3 - "$state" "$1" "$2" <<'PY'
import json, socket, sys
state, path, key = sys.argv[1:]
tok = json.load(open(state + "/private/operator.token"))["token"]
s = socket.socket(socket.AF_UNIX); s.connect(state + "/management.sock")
s.sendall(f"GET /v1/{path} HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer {tok}\r\nConnection: close\r\n\r\n".encode())
raw = b""
while (c := s.recv(65536)): raw += c
head, _, body = raw.partition(b"\r\n\r\n")
if b"chunked" in head.lower():
    out, rest = b"", body
    while rest:
        n, _, rest = rest.partition(b"\r\n"); n = int(n.split(b";")[0], 16)
        if n == 0: break
        out, rest = out + rest[:n], rest[n + 2:]
    body = out
print(json.loads(body)["data"][key])
PY
}

echo "==> Building the extension package"
make -C "$root/extension" package >/dev/null
"$oc" extension check "$root/extension/dist/assist"

echo "==> Installing it"
if "$oc" extension list | grep -q '^  assist '; then
	"$oc" extension update "$root/extension/dist/assist"
else
	"$oc" extension install "$root/extension/dist/assist"
fi

echo "==> The assistant's home"
mkdir -p "$root/home/memory" "$root/home/inbox" "$root/home/threads" "$root/home/extensions"
[ -f "$root/home/CLAUDE.md" ] || cp "$root/config/home-CLAUDE.md" "$root/home/CLAUDE.md"

echo "==> Restarting Open Conduit so it loads the package"
"$oc" service stop >/dev/null
"$oc" service start >/dev/null
i=0
until api health state >/dev/null 2>&1; do
	i=$((i + 1)); [ $i -gt 60 ] && { echo "Open Conduit didn't come back; see $state/logs/service.log" >&2; exit 1; }
	sleep 1
done

cfg=$("$oc" config path)
need_assist=1; need_repo=1
"$oc" config get /scopes/assist >/dev/null 2>&1 && need_assist=0
"$oc" config get /scopes/assist-alex >/dev/null 2>&1 && need_repo=0
if [ $need_assist = 1 ] || [ $need_repo = 1 ]; then
	# Apply only our changes: refuse when the file has edits the daemon
	# hasn't applied, rather than activating them unseen.
	active=$(api config hash)
	file=$("$oc" config validate "$cfg" | python3 -c 'import json,sys; print(json.load(sys.stdin)["candidate_hash"])')
	if [ "$active" != "$file" ]; then
		echo "The config file differs from the active generation; apply or revert those edits first." >&2
		exit 1
	fi
	cp "$cfg" "$cfg.before-assist"
	if [ $need_assist = 1 ]; then
		echo "==> Adding the assist scope (the assistant's threads)"
		"$oc" config set /scopes/- "$(sed "s#HOME_DIR#$root/home#" "$root/config/scope.json")"
	fi
	if [ $need_repo = 1 ]; then
		echo "==> Adding the assist-alex scope (issues on the assistant's own repository)"
		"$oc" config set /scopes/- "$(sed "s#/Users/alexyoung/code/assist-alex#$root#" "$root/config/github-scope.json")"
	fi
	"$oc" config apply "$cfg" --generation "$(api config generation)"
fi

echo "==> Building Assist.app"
"$root/scripts/build-app.sh"
echo "Done. Open ~/Applications/Assist.app"
