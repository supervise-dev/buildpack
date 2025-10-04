#!/bin/bash
mkdir -p /tmp/ttyd
pkgx +tmux -- ttyd \
  -W \
  -i /tmp/ttyd/ttyd.sock \
  -H X-WEBAUTH-USER \
  tmux -2 -u new -A -s session pkgx npx @anthropic-ai/claude-code@latest
