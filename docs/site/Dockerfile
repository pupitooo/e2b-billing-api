FROM scalarapi/api-reference@sha256:720f243b22561e621bf4a87b23595ed2f7aeae366ff38be1fe68bde8ef4aaa9d AS scalar
FROM squidfunk/mkdocs-material@sha256:51b87149d227691486b5f08993d28c65ca7e4990010664b697265b8e6fcd5287

COPY --from=scalar /usr/bin/caddy /usr/bin/caddy
WORKDIR /portal
ENV XDG_CONFIG_HOME=/tmp/caddy-config XDG_DATA_HOME=/tmp/caddy-data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["sh", "/source/site/serve.sh"]
