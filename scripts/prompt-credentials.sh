#!/bin/zsh
# Prompt for LoveACE credentials via osascript and write .env.test (chmod 600).
# The values are never echoed to the terminal or logged.
set -eu
cd "$(dirname "$0")/.."

ask() {
  osascript -e "text returned of (display dialog \"$1\" default answer \"\" $2 with title \"LoveACE Go 实网测试\")"
}

ACCOUNT=$(ask "学号" "")
PASSWORD=$(ask "统一认证（教务）密码" "with hidden answer")
GATEWAY=$(ask "VPN 网关密码（留空则与上面相同）" "with hidden answer")

umask 077
{
  print -r -- "LOVEACE_ACCOUNT=$ACCOUNT"
  print -r -- "LOVEACE_PASSWORD=$PASSWORD"
  print -r -- "LOVEACE_GATEWAY_PASSWORD=$GATEWAY"
} > .env.test
chmod 600 .env.test
print -r -- "written .env.test" >&2
