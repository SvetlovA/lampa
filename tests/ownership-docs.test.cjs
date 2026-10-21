// Run with: node --test tests/ownership-docs.test.cjs
// Guard the published API, migration SQL and scope examples against implementation drift.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const read = name => fs.readFileSync(path.join(__dirname, '..', name), 'utf8');
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

test('scope guard rejects local selector export and missing portable fields', () => {
  const document = read(documents[1]);
  const scopes = JSON.parse(read('svtlv/storage-scope.json')).scopes;
  assert.throws(() => checkScopes(document.replace(
    '| `user_connection` | `torrserver_url`,', '| `user_connection` | `torrserver_use_link`,'), scopes),
  /torrserver_use_link is not classified as user_connection/);
  assert.throws(() => checkScopes(document.replace('`torrserver_auth`, ', ''), scopes),
    /user_connection allowlist must be complete/);
  assert.throws(() => checkScopes(document.replace('`torrserver_url_two`, ', ''), scopes),
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

function userDataSQL(document) {
  const sql = document.match(/create table lampa_user_data\s*\([\s\S]*?\);/);
  assert.ok(sql, 'current user-data DDL must be documented');
  return sql[0].replace(/\s+/g, ' ').trim();
}

function userDataExamples(document) {
  return [...document.matchAll(/```json\n([\s\S]*?)\n```/g)]
    .map(match => JSON.parse(match[1])).filter(value => Object.hasOwn(value, 'schema_version'));
}

function checkConnectionExample(example) {
  const scope = JSON.parse(read('svtlv/storage-scope.json'));
  const keys = Object.keys(example.data).filter(key => key.startsWith('torrserver_'));
  assert.deepEqual(keys.sort(), [...scope.scopes.user_connection].sort(),
    'example must include both URLs and shared authentication, without the local selector');
  assert.deepEqual([...example.encrypt].sort(), [...scope.encrypt].sort(),
    'example must seal the shared credentials');
}

test('both published PUT examples include both URLs and shared sealed credentials, never the selector', () => {
  for (const file of documents) {
    for (const example of userDataExamples(read(file))) checkConnectionExample(example);
  }
});

test('connection example guard detects a missing secondary URL, exported selector or unsealed credentials', () => {
  const example = userDataExamples(read(documents[0]))[0];
  const missingURL = structuredClone(example);
  delete missingURL.data.torrserver_url_two;
  assert.throws(() => checkConnectionExample(missingURL), /both URLs/);
  const exportedSelector = structuredClone(example);
  exportedSelector.data.torrserver_use_link = 'two';
  assert.throws(() => checkConnectionExample(exportedSelector), /local selector/);
  assert.throws(() => checkConnectionExample({...example, encrypt: []}), /seal the shared credentials/);
});

function checkCurrentLinkGuidance(document) {
  // The design's Plan 3 implementation record deliberately retains its historical decisions.
  const current = document.replace(/### Plan 3:[\s\S]*?(?=### Plan 4:)/, '').replace(/\s+/g, ' ');
  assert.doesNotMatch(current,
    /(?:sole TorrServer URL|only one TorrServer URL input remains|drops the second TorrServer input|reapply the (?:small, marked |marked )?one-link (?:bundle|core) patch)/i,
    'current guidance must not restore the superseded one-link policy');
  for (const key of ['torrserver_url', 'torrserver_url_two', 'torrserver_use_link']) {
    assert.ok(current.includes('`' + key + '`'), `${key} must be documented`);
  }
  assert.match(current, /device-local[^.]*`one`/, 'document the local selector and its default');
  assert.match(current, /selector-only change sends no user-data PUT/i, 'document selection without uploads');
  assert.match(current, /whole-document writes can omit (?:URL 2|it)/i, 'document older-client compatibility');
  assert.ok(current.includes('`Lampa.Torserver.ip()`'), 'document the public effective-server identity');
}

test('operator and maintenance guidance retain the two-link sync and effective-server contract', () => {
  for (const file of [...documents, 'CLAUDE.md']) checkCurrentLinkGuidance(read(file));
  assert.match(read('CLAUDE.md'), /TestTorrserverTwoURLs_matchBundle/);
});

test('current-guidance guard rejects obsolete single-link update instructions while allowing history', () => {
  const document = read(documents[1]);
  checkCurrentLinkGuidance(document);
  for (const obsolete of [
    'Reapply the marked one-link core patch after upstream updates.',
    'Only one TorrServer URL input remains.',
    'The sole TorrServer URL is portable.'
  ]) {
    assert.throws(() => checkCurrentLinkGuidance(document + '\n' + obsolete), /superseded one-link policy/);
  }
});

test('published user-data examples use the implemented schema and valid raw-storage envelopes', () => {
  const schema = Number(read('backend/pkg/storage/document.go').match(/const SchemaVersion = (\d+)/)[1]);
  const scope = JSON.parse(read('svtlv/storage-scope.json'));
  for (const file of documents) {
    const examples = userDataExamples(read(file));
    assert.ok(examples.length, `${file}: a complete user-data example is required`);
    for (const example of examples) {
      assert.equal(example.schema_version, schema, file);
      assert.equal(typeof example.data, 'object', file);
      assert.ok(example.data && !Array.isArray(example.data), file);
      assert.ok(Array.isArray(example.encrypt) && example.encrypt.length <= 64, file);
      for (const key of example.encrypt) assert.match(key, /^[A-Za-z0-9_.:-]{1,128}$/, file);
      for (const [key, value] of Object.entries(example.data)) {
        assert.match(key, /^[A-Za-z0-9_.:-]{1,128}$/, file);
        assert.ok(['user', 'user_connection', 'user_optional'].some(name => scope.scopes[name].includes(key)),
          `${file}: example key ${key} must be portable`);
        assert.equal(typeof value, 'string', `${file}: add-on values must be raw storage strings`);
      }
      assert.deepEqual(JSON.parse(example.data.favorite), {book: [], history: []}, file);
    }
  }
});

test('design user-data DDL matches the initial schema with the v2 migration applied', () => {
  const initial = userDataSQL(read('backend/migrations/00001_create_lampa_user_data.sql'));
  const migration = read('backend/migrations/00003_user_data_v2.sql').split('-- +goose Down')[0];
  const dropped = migration.match(/drop column (\w+)/)[1];
  const added = migration.match(/add column ([^;]+);/)[1];
  const expected = initial.replace(new RegExp(` ${dropped} [^,]+,`), ` ${added},`);
  assert.equal(userDataSQL(read(documents[1])), expected);
  assert.notEqual(userDataSQL(read(documents[1]).replace('etag text not null', 'etag text')), expected);
});

test('both documents publish current user-data response statuses and body limit', () => {
  const limit = JSON.parse(read('backend/pkg/config/defaults/appsettings.json')).Api.MaxBodyBytes;
  for (const file of documents) {
    const document = read(file);
    assert.ok(document.includes(String(limit)), `${file}: configured body limit must be documented`);
    for (const [method, statuses] of [['GET', [200, 304]], ['PUT', [204]], ['DELETE', [204]]]) {
      const row = document.split('\n').find(line => line.startsWith(`| \`${method} /api/v1/user-data\``));
      assert.ok(row, `${file}: ${method} user-data route must be documented`);
      for (const status of statuses) assert.match(row, new RegExp(`\\b${status}\\b`), `${file}: ${method} ${status}`);
      if (method === 'PUT') assert.doesNotMatch(row, /\b200\b/, `${file}: PUT no longer returns a document`);
    }
    for (const header of ['X-Lampa-Expected-User', 'If-None-Match', 'If-Match', 'ETag']) {
      assert.ok(document.includes('`' + header + '`'), `${file}: ${header} must be documented`);
    }
    assert.ok(document.includes('412 precondition_failed'), `${file}: stale saves must be documented`);
    assert.doesNotMatch(document, /500 connections_unreadable|schema_version` 1 is accepted/);
  }
});
