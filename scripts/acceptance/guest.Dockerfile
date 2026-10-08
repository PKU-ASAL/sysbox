FROM alpine:3.22
COPY sysbox-guest-task /usr/local/bin/sysbox-guest-task
COPY issuer.pub /etc/sysbox/grant-public-key
RUN chmod 0755 /usr/local/bin/sysbox-guest-task && chmod 0600 /etc/sysbox/grant-public-key
ENTRYPOINT ["sleep"]
CMD ["infinity"]
