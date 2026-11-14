#!/bin/sh
# Start-up of a firewall helper: put the rules in place, mark the helper as
# ready, then stay alive so the network (and its rules) keeps existing.
set -eu

rm -f /tmp/fw-ready
fw-apply
touch /tmp/fw-ready

echo "firewall for ${FW_NAME} is in place:"
iptables -S

exec sleep infinity
