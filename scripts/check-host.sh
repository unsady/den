#!/bin/sh
# Runs den on this Linux machine the way a new user would, with docker or
# podman, up to the question for a Telegram token, then checks the commands
# that drive the container. It uses the published image and the den binary
# given, and leaves a container, volume, and ~/den behind.
#
#   scripts/check-host.sh docker ./den
set -eu

runtime=$1
den=$2

step() { printf '\n== %s\n' "$*"; }
fail() {
	printf 'check-host: %s\n' "$*" >&2
	"$runtime" logs den 2>&1 | tail -30 >&2 || true
	exit 1
}

# With Docker present, den would pick it first; the podman run names its
# tool in the settings, as a user with both installed would.
if [ "$runtime" != docker ]; then
	mkdir -p ~/.config/den
	printf '{"runtime": "%s", "image": "ghcr.io/unsady/den:latest"}\n' "$runtime" >~/.config/den/config.json
fi

step "den setup, with a token Telegram refuses"
out=$(printf '\nnot-a-token\n' | "$den" setup 2>&1) && fail "setup accepted a fake token"
echo "$out"
echo "$out" | grep -q "Telegram does not accept this token" || fail "setup stopped before the token"
grep -q "\"runtime\": \"$runtime\"" ~/.config/den/config.json || fail "setup did not save runtime $runtime"

step "status"
"$den" status | tee /dev/stderr | grep -q "Container den" || fail "den is not running"

step "secrets"
printf 'value\n' | "$den" secret set check_host
"$den" secret ls | grep -qx check_host || fail "the secret was not kept"
"$den" secret rm check_host

step "the agent and its home"
"$den" fx --version
"$runtime" exec -u agent den touch /home/agent/space-check || fail "the agent cannot write its home"
[ -e ~/den/space-check ] || fail "the agent's home is not ~/den"

step "restart keeps secrets"
printf 'value\n' | "$den" secret set check_host
"$den" restart
"$den" secret ls | grep -qx check_host || fail "the secret did not survive a restart"

step "stop and start"
"$den" stop
"$den" status | grep -q "not running" || fail "den did not stop"
"$den" start
"$den" status | grep -q "Container den" || fail "den did not start again"

echo
echo "den runs with $runtime"
