#!/bin/sh
# Prints the version to stamp into the extension package and Assist.app.
# VERSION holds the release; a build of the commit tagged v<VERSION> gets
# exactly that, any other build gets the short SHA as build metadata
# (0.1.0+611ceeb), and uncommitted changes add -dirty (0.1.0+611ceeb-dirty).
# Fails rather than guess when VERSION or git can't say which code this is.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
release=$(tr -d ' \t\r\n' < "$root/VERSION")
case $release in
'' | *[!0-9.]* | .* | *. | *..*)
	echo "version.sh: VERSION must be a release like 1.2.3, not '$release'" >&2
	exit 1 ;;
esac
if ! sha=$(git -C "$root" rev-parse --short HEAD 2>/dev/null); then
	echo "version.sh: can't read the commit from git in $root" >&2
	exit 1
fi
dirty=
git -C "$root" update-index -q --refresh >/dev/null 2>&1 || true
[ -z "$(git -C "$root" status --porcelain 2>/dev/null)" ] || dirty=-dirty
if [ -z "$dirty" ] && [ "$(git -C "$root" rev-parse -q --verify "refs/tags/v$release^{commit}" 2>/dev/null)" = "$(git -C "$root" rev-parse HEAD)" ]; then
	echo "$release"
else
	echo "$release+$sha$dirty"
fi
