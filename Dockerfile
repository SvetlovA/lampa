FROM httpd:alpine3.15
ARG domain
ARG prefix="http://"
RUN test -n "$domain" || (echo "ERROR: domain is not set" && false)
# same-origin /api/v1/ proxy to lampa-api, written here so no config directory ships in htdocs.
# disablereuse: this httpd resolves the backend once per worker, so reused connections would keep
# a restarted lampa-api's old IP and answer 502; only /api/v1/ is proxied, health 8081 never is
RUN conf=/usr/local/apache2/conf/httpd.conf \
 && sed -i -e 's|^#\(LoadModule proxy_module \)|\1|' -e 's|^#\(LoadModule proxy_http_module \)|\1|' -e 's|^#\(LoadModule headers_module \)|\1|' "$conf" \
 && grep -q '^LoadModule proxy_module ' "$conf" && grep -q '^LoadModule proxy_http_module ' "$conf" && grep -q '^LoadModule headers_module ' "$conf" \
 && printf '%s\n' \
    '' \
    'ProxyPreserveHost On' \
    'RequestHeader set X-Lampa-Proto "expr=%{REQUEST_SCHEME}"' \
    'ProxyPass /api/v1/ http://lampa-api:5800/api/v1/ disablereuse=On' \
    'ProxyPassReverse /api/v1/ http://lampa-api:5800/api/v1/' \
    >> "$conf"
COPY . /usr/local/apache2/htdocs/
RUN sed -i -e "s|{domain}|$domain|" -e "s|{PREFIX}|$prefix|" /usr/local/apache2/htdocs/msx/start.json
