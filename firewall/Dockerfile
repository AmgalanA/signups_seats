# Firewall helper: a tiny container that owns the network of one service
# and holds the iptables rules for it.
FROM alpine:3.20
RUN apk add --no-cache iptables conntrack-tools
COPY entrypoint.sh fw-apply fw-remove /usr/local/bin/
# Strip Windows line endings in case an editor added them, then make runnable.
RUN cd /usr/local/bin \
    && sed -i 's/\r$//' entrypoint.sh fw-apply fw-remove \
    && chmod +x entrypoint.sh fw-apply fw-remove
ENTRYPOINT ["entrypoint.sh"]
