// Run with: node --test docs/tests/ownership-docs.test.cjs
// Guard the published API, migration SQL and scope examples against implementation drift.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const read = name => fs.readFileSync(path.join(__dirname, '../..', name), 'utf8');
const documents = ['backend/README.md', 'docs/settings-sync-backend-design.md'];

function routesIn(document) {
  return [...document.matchAll(/\| `(GET|PUT|DELETE) (\/api\/v1\/torrservers\/[^`]+)` \|/g)]
    .map(match => `${match[1]} ${match[2]}`).sort();
}

function implementedRoutes() {
  const constants = {};
  for (const match of read('backend/pkg/api/torrents.go').matchAll(
    /^\s*(torrserversPrefix|torrentHashesPath|torrentHashPath)\s*=\s*(.+)$/gm)) {
    constants[match[1]] = match[2].split('+').map(part => {
      const token = part.trim();
      return token.startsWith('"') ? JSON.parse(token) : constants[token];
    }).join('');
  }
  return [...read('backend/pkg/api/server.go').matchAll(
    /mux\.HandleFunc\("(GET|PUT|DELETE) "\+(torrentHashesPath|torrentHashPath),/g)]
    .map(match => `${match[1]} ${constants[match[2]]}`).sort();
}

function membershipSQL(document) {
  const sql = document.match(/create table lampa_torrserver_hashes\s*\([\s\S]*?\);/);
  assert.ok(sql, 'membership table DDL must be documented');
  return sql[0].replace(/\s+/g, ' ').trim();
}

function checkScopes(document, scopes) {
  const section = document.match(/### 10\.3[^\n]*\n([\s\S]*?)\n### 10\.4/);
  assert.ok(section, 'storage audit section must exist');
  const rows = [...section[1].matchAll(/^\| `(\w+)` \| (.+) \|$/gm)];
  assert.deepEqual(rows.map(row => row[1]).sort(), Object.keys(scopes).sort());
  for (const [, scope, description] of rows) {
    const keys = [...description.matchAll(/`([^`]+)`/g)].map(match => match[1]);
    assert.ok(keys.length, `${scope} must have concrete examples`);
    for (const key of keys) {
      assert.ok(scopes[scope].includes(key), `${key} is not classified as ${scope}`);
    }
    if (scope === 'user' || scope === 'user_connection' || scope === 'user_optional') {
      assert.deepEqual(keys.sort(), [...scopes[scope]].sort(), `${scope} allowlist must be complete`);
    }
  }
}

test('both API tables document every implemented membership route without extra routes', () => {
  const routes = implementedRoutes();
  assert.equal(routes.length, 3, 'must discover the authenticated membership routes');
  for (const file of documents) assert.deepEqual(routesIn(read(file)), routes, file);
});

test('API documentation guard detects a missing or stale route', () => {
  const routes = implementedRoutes();
  const document = read(documents[0]);
  assert.notDeepEqual(routesIn(document.replace('`PUT /api/v1/torrservers/', '`PATCH /api/v1/torrservers/')), routes);
  assert.notDeepEqual(routesIn(document.replace('{server_id}/hashes`', '{server_id}/torrents`')), routes);
});

test('both documented membership schemas match the applied migration, including constraints', () => {
  const sql = membershipSQL(read('backend/migrations/00002_create_lampa_torrserver_hashes.sql'));
  for (const file of documents) assert.equal(membershipSQL(read(file)), sql, file);
});

test('schema guard detects a missing table or weakened ownership key', () => {
  const document = read(documents[0]);
  assert.throws(() => membershipSQL('no SQL here'), /DDL must be documented/);
  assert.notEqual(membershipSQL(document.replace('primary key (user_id, server_id, hash)',
    'primary key (server_id, hash)')), membershipSQL(document));
});

test('design scope examples and complete portable allowlists match the machine-readable inventory', () => {
  checkScopes(read(documents[1]), JSON.parse(read('svtlv/storage-scope.json')).scopes);
});

test('scope guard rejects legacy connection export and missing portable fields', () => {
  const document = read(documents[1]);
  const scopes = JSON.parse(read('svtlv/storage-scope.json')).scopes;
  assert.throws(() => checkScopes(document.replace(
    '| `user_connection` | `torrserver_url`,', '| `user_connection` | `torrserver_url_two`,'), scopes),
  /torrserver_url_two is not classified as user_connection/);
  assert.throws(() => checkScopes(document.replace('`torrserver_auth`, ', ''), scopes),
    /user_connection allowlist must be complete/);
});

test('scope guard detects missing best-effort fields and exported CUB profile progress', () => {
  const document = read(documents[1]);
  const scopes = JSON.parse(read('svtlv/storage-scope.json')).scopes;
  assert.throws(() => checkScopes(document.replace('`file_view`, ', ''), scopes),
    /user_optional allowlist must be complete/);
  assert.throws(() => checkScopes(document.replace(
    '| `user_optional` | `file_view`,', '| `user_optional` | `file_view_*`,'), scopes),
  /file_view_\* is not classified as user_optional/);
  assert.throws(() => checkScopes(document.replace(/^\| `user_optional`.*\n/m, ''), scopes));
});
