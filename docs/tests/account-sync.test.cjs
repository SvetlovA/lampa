// Run the actual ES5 add-on with controlled storage and network responses.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../../svtlv/account-sync.js'), 'utf8');
const realScope = JSON.parse(fs.readFileSync(path.join(__dirname, '../../svtlv/storage-scope.json'), 'utf8'));
const BASE = 'svtlv_sync_base', USER = 'svtlv_sync_user';
const LIMIT = 4 * 1024 * 1024;
const clean = value => JSON.parse(JSON.stringify(value));
// Inspect private core functions without adding a production debug/public API.
const inspected = source.replace(/\}\)\(\);\s*$/, `window.core = {
  request: request, loadScope: loadScope, classify: classify, synced: synced,
  fp: fp, dirty: dirty, readBase: readBase, writeBase: writeBase,
  exportLocal: exportLocal, overlay: overlay, applyDoc: applyDoc,
  clearSynced: clearSynced, fitBody: fitBody,
  disabled: function () { return disabled; }, failure: function () { return failure; }
}; })();`);

function page(options = {}) {
  const items = Object.assign(Object.create(null), options.items);
  const faults = {}, calls = [], requests = [], notices = [];
  function check(method, key) {
    calls.push({method, key});
    if (faults[method]) throw new Error(method);
    if (method === 'setItem' && faults.quota === key) {
      const error = new Error('quota'); error.name = 'QuotaExceededError'; throw error;
    }
    if (method === 'getItem' && faults.read === key) throw new Error('read');
  }
  const store = {
    get length() { check('length'); return Object.keys(items).length; },
    key(index) { check('key'); return Object.keys(items)[index] ?? null; },
    getItem(key) { check('getItem', key); return items[key] ?? null; },
    setItem(key, value) { check('setItem', key); if (!faults.silent) items[key] = String(value); },
    removeItem(key) { check('removeItem', key); if (!faults.silentRemove) delete items[key]; }
  };
  const window = {
    location: {protocol: options.protocol || 'https:'},
    Lampa: {Storage: {get: () => options.language || 'en'}, Noty: {show: text => notices.push(text)}}
  };
  if (options.preboot) delete window.Lampa;
  Object.defineProperty(window, 'localStorage', {
    get() { if (faults.property) throw new Error('localStorage getter'); return store; }
  });
  const $ = {ajax(config) {
    if (faults.ajax) throw new Error('ajax');
    const xhr = {status: 200, getResponseHeader: name => name === 'ETag' ? '"etag"' : null};
    const request = {
      config,
      success(data, status = 200) { xhr.status = status; config.success(data, 'success', xhr); },
      fail(status = 503) { config.error({...xhr, status}); }
    };
    requests.push(request);
    return request;
  }};
  const context = vm.createContext({window, $, cache_version: 42});
  vm.runInContext(inspected, context);
  return {
    window, core: window.core, items, faults, calls, requests, notices,
    reload() { vm.runInContext(inspected, context); },
    load(scope = realScope) {
      let result;
      window.core.loadScope(ok => { result = ok; });
      requests.at(-1).success(clean(scope));
      return result;
    },
    syncedBase(data) {
      items[BASE] = JSON.stringify(Object.fromEntries(Object.entries(data)
        .filter(([, value]) => value !== null && value !== '')
        .map(([key, value]) => [key, window.core.fp(value)])));
    }
  };
}

test('protocol and duplicate-load guards leave boot and storage alone', () => {
  for (const protocol of ['file:', 'httpx:', 'data:']) {
    const p = page({protocol});
    assert.equal(p.window.svtlv_sync_loaded, undefined);
    assert.equal(p.core, undefined);
    assert.equal(p.calls.length, 0);
    assert.equal(p.requests.length, 0);
  }
  const p = page({protocol: 'http:'});
  assert.equal(p.window.svtlv_sync_loaded, true);
  p.reload();
  assert.equal(p.window.core, p.core);
  assert.equal(p.requests.length, 0, 'the core alone never starts account requests');
});

test('scope fetch uses the loader cache version and classifies the real inventory', () => {
  const p = page();
  assert.equal(p.load(), true);
  assert.equal(p.requests[0].config.url, 'svtlv/storage-scope.json?v=42');
  assert.equal(p.requests[0].config.timeout, 15000);
  const cases = [
    ['favorite', 'user'], ['language', 'user'], ['torrserver_login', 'user_connection'],
    ['file_view', 'user_optional'], ['file_view_123', 'external'],
    ['account', 'external'], ['account_token', ''], ['account_timeline_sync_12', 'external'],
    ['account_bookmarks_sync', 'external'], ['search_history', 'user_optional'],
    ['search_other', 'cache'], ['developer_enabled', 'device'], ['developer_param_test', 'cache'],
    ['player_normalization_step_1', 'device'], ['plugins', 'device'], ['jackett_key', 'connection'],
    ['content_rows', 'device'], ['content_rows_movie', 'device'], ['unknown', ''], ['xaccount', '']
  ];
  for (const [key, expected] of cases) assert.equal(p.core.classify(key), expected, key);
  for (const [key, expected] of cases) {
    assert.equal(p.core.synced(key), ['user', 'user_connection', 'user_optional'].includes(expected), key);
  }
});

test('Go resolver case table: exact entries beat prefixes and longest prefix wins', () => {
  const scope = clean(realScope);
  for (const name of Object.keys(scope.scopes)) scope.scopes[name] = [];
  scope.scopes.external = ['account*'];
  scope.scopes.cache = ['account_timeline*'];
  scope.scopes.user = ['account_timeline_progress'];
  scope.scopes.device = ['account_timeline_sync*'];
  const p = page();
  p.load(scope);
  for (const [key, expected] of [
    ['account', 'external'], ['account_token', 'external'],
    ['account_timeline_progress', 'user'], ['account_timeline_other', 'cache'],
    ['account_timeline_sync_12', 'device'], ['unknown', ''], ['xaccount', '']
  ]) assert.equal(p.core.classify(key), expected, key);
});

test('sync key pattern excludes wildcard imports, punctuation and oversized keys', () => {
  const scope = clean(realScope);
  scope.scopes.user.push('custom*', '__proto__', 'constructor', 'toString');
  const p = page(); p.load(scope);
  for (const key of ['custom.key:v1-ok', '__proto__', 'constructor', 'toString']) assert.equal(p.core.synced(key), true);
  for (const key of ['custom space', 'custom*', 'custom/' , `custom${'x'.repeat(128)}`, null]) {
    assert.equal(p.core.synced(key), false);
  }
  const input = JSON.parse('{"__proto__":"value","constructor":"ctor","toString":"str"}');
  p.core.applyDoc(input);
  assert.deepEqual(clean(p.core.exportLocal()), input);
  assert.deepEqual(clean(p.core.overlay(input)), input);
});

test('fingerprints use FNV-1a; empty and absent are the same dirty state', () => {
  const p = page(); p.load();
  assert.equal(p.core.fp(null), null);
  assert.equal(p.core.fp(''), null);
  assert.equal(p.core.fp('a'), '1:e40c292c');
  assert.equal(p.core.fp('hello'), '5:4f9f2cab');
  assert.notEqual(p.core.fp('ru'), p.core.fp('en'));
  assert.equal(p.core.dirty('language'), false);
  p.items.language = ''; assert.equal(p.core.dirty('language'), false);
  p.items.language = 'ru'; assert.equal(p.core.dirty('language'), true);
  const q = page({items: {language: 'ru'}}); q.load(); q.syncedBase({language: 'ru'});
  assert.equal(q.core.dirty('language'), false);
  q.items.language = ''; assert.equal(q.core.dirty('language'), true);
  delete q.items.language; assert.equal(q.core.dirty('language'), true);
});

test('export/apply round trip preserves exact raw strings and persists the base', () => {
  const data = {
    language: 'ru', tmdb_lang: '001', torrserver_auth: 'false', torrserver_password: ' "\\\nпароль😀',
    favorite: ' { "cards": [1, true, null] } ', file_view: '{"x":0.5}',
    online_view: '[1,"2"]', mine_reactions: '[malformed JSON'
  };
  const p = page({items: {...data, plugins: '["code"]', search_history: '', other: 'local'}}); p.load();
  assert.deepEqual(clean(p.core.exportLocal()), data);
  const q = page(); q.load();
  assert.equal(q.core.applyDoc(data).changed, true);
  assert.deepEqual(clean(q.core.exportLocal()), data);
  assert.deepEqual(JSON.parse(q.items[BASE]), Object.fromEntries(Object.entries(data).map(([k, v]) => [k, q.core.fp(v)])));
  assert.equal(q.core.applyDoc(data).changed, false);
  const restarted = page({items: q.items}); restarted.load();
  for (const key of Object.keys(data)) assert.equal(restarted.core.dirty(key), false, key);
});

test('apply ignores non-synced/non-string values, preserves dirty edits, and updates equal values', () => {
  const p = page({items: {language: 'ru', favorite: '{"old":1}', plugins: 'local'}}); p.load();
  p.syncedBase({language: 'en', favorite: '{"old":1}'});
  const result = p.core.applyDoc({language: 'en', favorite: '{"new":2}', plugins: 'remote',
    tmdb_lang: true, torrserver_url: 42, file_view: {not: 'string'}, online_view: null});
  assert.equal(result.changed, true);
  assert.equal(p.items.language, 'ru');
  assert.equal(p.items.favorite, '{"new":2}');
  assert.equal(p.items.plugins, 'local');
  assert.equal(p.items.tmdb_lang, undefined);
  assert.equal(p.items.file_view, undefined);
  assert.equal(p.core.dirty('language'), true);
  assert.equal(JSON.parse(p.items[BASE]).favorite, p.core.fp('{"new":2}'));
  assert.equal(p.core.dirty('favorite'), false);
});

test('overlay keeps the latest server copy and overlays only dirty local strings', () => {
  const p = page({items: {language: 'ru', tmdb_lang: 'en', torrserver_url: 'http://new', plugins: 'local'}});
  p.load(); p.syncedBase({tmdb_lang: 'en', language: 'en'});
  const server = {language: 'de', tmdb_lang: 'fr', favorite: '{"new":1}', plugins: 'remote', file_view: 42};
  assert.deepEqual(clean(p.core.overlay(server)), {
    language: 'ru', tmdb_lang: 'fr', favorite: '{"new":1}', torrserver_url: 'http://new'
  });
  assert.equal(server.language, 'de', 'input is not mutated');
});

test('clear writes empty tombstones, masks reserve-only keys, and removes marker/base', () => {
  const p = page({items: {favorite: '{"book":[1]}', file_view: '{}', plugins: 'code', player: 'native',
    file_view_123: 'cub', [USER]: 'Alice'}});
  p.load(); p.syncedBase({favorite: p.items.favorite});
  assert.equal(p.core.clearSynced(), true);
  const reserve = {favorite: 'old favorites', language: 'old language'};
  const upstreamGet = key => p.items[key] == null ? reserve[key] : p.items[key];
  assert.equal(upstreamGet('favorite'), '');
  assert.equal(upstreamGet('language'), '', 'even absent local items mask reserve');
  assert.equal(p.items.file_view, '');
  assert.equal(p.items.plugins, 'code');
  assert.equal(p.items.player, 'native');
  assert.equal(p.items.file_view_123, 'cub');
  assert.equal(p.items[USER], undefined);
  assert.equal(p.items[BASE], undefined);
  assert.equal(p.core.clearSynced(), false);
  assert.equal(p.core.dirty('favorite'), false);
});

test('must-sync values apply first; optional quota failure leaves its old base for retry', () => {
  const p = page({items: {file_view: 'old'}}); p.load(); p.syncedBase({file_view: 'old'});
  p.faults.quota = 'file_view';
  assert.equal(p.core.applyDoc({file_view: 'new', language: 'ru', torrserver_url: 'url'}).changed, true);
  const writes = p.calls.filter(c => c.method === 'setItem').map(c => c.key);
  assert.ok(writes.indexOf('language') < writes.indexOf('file_view'));
  assert.ok(writes.indexOf('torrserver_url') < writes.indexOf('file_view'));
  assert.equal(p.items.file_view, 'old');
  assert.equal(JSON.parse(p.items[BASE]).file_view, p.core.fp('old'));
  assert.equal(p.core.disabled(), false);
  delete p.faults.quota;
  p.core.applyDoc({file_view: 'new'});
  assert.equal(p.items.file_view, 'new');
});

test('must-sync quota failure stops apply and disables all subsequent requests', () => {
  const p = page(); p.load(); p.faults.quota = 'language';
  assert.throws(() => p.core.applyDoc({language: 'ru', file_view: '{}'}), /quota/);
  assert.equal(p.items.file_view, undefined);
  assert.equal(p.core.disabled(), true);
  let ok; p.core.request('PUT', '/api/v1/user-data', {}, value => { ok = value; });
  assert.equal(ok, false);
  assert.equal(p.requests.length, 1);
  assert.deepEqual(p.notices, ['Account sync is unavailable on this device']);
});

test('every storage operation and silent writes fail closed, including before Lampa exists', () => {
  for (const [fault, invoke] of [
    ['property', p => p.core.exportLocal()], ['length', p => p.core.exportLocal()],
    ['key', p => p.core.exportLocal()], ['getItem', p => p.core.dirty('language')],
    ['setItem', p => p.core.applyDoc({language: 'ru'})],
    ['removeItem', p => p.core.clearSynced()], ['silent', p => p.core.applyDoc({language: 'ru'})]
  ]) {
    const p = page({preboot: true, items: {plugins: 'local'}}); p.load(); p.faults[fault] = true;
    assert.throws(() => invoke(p), undefined, fault);
    assert.equal(p.core.disabled(), true, fault);
    assert.equal(p.items.plugins, 'local');
  }
  const p = page(); p.load(); p.faults.read = 'file_view';
  assert.throws(() => p.core.applyDoc({file_view: 'new'}), /read/);
  assert.equal(p.core.disabled(), true, 'optional read failures are not quota skips');
});

test('malformed persisted base disables sync instead of reclassifying edits as clean', () => {
  for (const raw of ['{', '[]', 'null', '{"language":42}', '{"language":"wrong"}']) {
    const p = page({items: {[BASE]: raw}}); p.load();
    assert.throws(() => p.core.dirty('language'));
    assert.equal(p.core.disabled(), true);
  }
});

test('silent metadata removal fails closed and repeated storage errors show one notice', () => {
  const p = page({items: {[USER]: 'Alice'}}); p.load(); p.faults.silentRemove = true;
  assert.throws(() => p.core.clearSynced(), /Storage removal failed/);
  assert.equal(p.core.disabled(), true);
  assert.equal(p.items[USER], 'Alice');
  const q = page(); q.load(); q.faults.getItem = true;
  assert.throws(() => q.core.dirty('language'));
  assert.throws(() => q.core.exportLocal());
  assert.deepEqual(q.notices, ['Account sync is unavailable on this device']);
});

test('size rule measures escaped UTF-8 body and drops largest encoded optional value first', () => {
  const p = page(); p.load();
  const data = {language: 'ru', torrserver_password: 'секрет',
    file_view: '\\'.repeat(1100000), search_history: 'a'.repeat(1500000), online_view: 'b'.repeat(1000000)};
  assert.ok(Buffer.byteLength(JSON.stringify({schema_version: 2, data, encrypt: realScope.encrypt})) > LIMIT);
  const body = p.core.fitBody(data);
  assert.equal(body.data.file_view, undefined, 'escaping makes file_view largest on the wire');
  assert.equal(body.data.search_history.length, 1500000);
  assert.equal(body.data.online_view.length, 1000000);
  assert.deepEqual(clean(body.encrypt), realScope.encrypt);
  assert.ok(Buffer.byteLength(JSON.stringify(body)) <= LIMIT);
  assert.equal(data.file_view.length, 1100000, 'input retained locally');
});

test('size includes envelope/encrypt and UTF-8; must-sync overflow blocks upload', () => {
  const p = page(); p.load();
  const envelope = {schema_version: 2, data: {language: ''}, encrypt: realScope.encrypt};
  const overhead = Buffer.byteLength(JSON.stringify(envelope));
  assert.ok(p.core.fitBody({language: 'x'.repeat(LIMIT - overhead)}));
  assert.equal(p.core.fitBody({language: 'x'.repeat(LIMIT - overhead + 1)}), null);
  assert.equal(p.core.fitBody({favorite: 'я'.repeat(LIMIT / 2), file_view: 'optional'}), null);
  assert.equal(p.core.disabled(), false, 'oversize edits can be corrected without restarting');
  assert.equal(p.core.fitBody({language: 'en'}).data.language, 'en');
  assert.ok(p.notices.every(text => text === 'Not saved to Account'));
});

test('lone surrogates and encoding failures skip uploads with a notice, valid pairs work', () => {
  const p = page(); p.load();
  for (const value of ['\ud800', '\udfff', 'prefix\ud800suffix']) {
    assert.equal(p.core.fitBody({favorite: value}), null);
  }
  assert.equal(p.core.fitBody({language: '😀'}).data.language, '😀');
  assert.equal(p.notices.length, 3);
  assert.equal(p.core.failure(), 'save');
});

test('body filters disallowed, empty and non-string data and accepts new allowlist keys', () => {
  const scope = clean(realScope); scope.scopes.user.push('new.setting');
  const p = page(); p.load(scope);
  const body = p.core.fitBody({'new.setting': 'yes', plugins: 'code', language: '', tmdb_lang: null, favorite: {a: 1}});
  assert.deepEqual(clean(body.data), {'new.setting': 'yes'});
});

test('invalid scope JSON disables sync and produces a localized notice', () => {
  const variants = [null, [], '{', {...realScope, version: 2}, {...realScope, scopes: {}},
    {...realScope, encrypt: 'secret'}, {...realScope, encrypt: Array(65).fill('key')},
    {...realScope, encrypt: ['bad key']}, {...realScope, encrypt: [42]}];
  for (const change of [
    s => { delete s.scopes.user_optional; }, s => { s.scopes.extra = []; },
    s => { s.scopes.user = null; }, s => { s.scopes.user = ['bad key']; },
    s => { s.scopes.user = ['*']; }, s => { s.scopes.user = ['key*tail']; },
    s => { s.scopes.user.push('plugins'); }, s => { s.scopes.user.push('language'); }
  ]) { const s = clean(realScope); change(s); variants.push(s); }
  for (const invalid of variants) {
    const p = page({language: 'ru'});
    assert.equal(p.load(invalid), false, JSON.stringify(invalid)?.slice(0, 80));
    assert.equal(p.core.disabled(), true);
    assert.deepEqual(p.notices, ['Не сохранено в Аккаунт']);
  }
});

test('scope network/parser failures preserve local data and allow a later fetch', () => {
  const p = page({items: {language: 'ru'}}); let result;
  p.core.loadScope(ok => { result = ok; });
  p.requests[0].fail(503);
  assert.equal(result, false);
  assert.equal(p.core.disabled(), false);
  assert.equal(p.items.language, 'ru');
  assert.equal(p.load(), true);
});

test('ajax helper sends JSON, timeout, conditional identity and CSRF headers', () => {
  const p = page();
  let result;
  p.core.request('GET', '/api/v1/user-data', {user: 'Alice', etag: '"old"', timeout: 3000},
    (ok, data, xhr) => { result = {ok, data, status: xhr.status}; });
  let request = p.requests[0];
  assert.equal(request.config.dataType, 'json');
  assert.equal(request.config.timeout, 3000);
  assert.deepEqual(clean(request.config.headers), {'X-Lampa-Expected-User': 'Alice', 'If-None-Match': '"old"'});
  request.success(undefined, 304);
  assert.deepEqual(result, {ok: true, data: undefined, status: 304});
  const body = {schema_version: 2, data: {language: 'ru'}, encrypt: realScope.encrypt};
  p.core.request('PUT', '/api/v1/user-data', {user: 'Alice', etag: '"old"', body},
    (ok, data, xhr) => { result = {ok, status: xhr.status}; });
  request = p.requests[1];
  assert.equal(request.config.data, JSON.stringify(body));
  assert.equal(request.config.contentType, 'application/json; charset=UTF-8');
  assert.equal(request.config.processData, false);
  assert.equal(request.config.dataType, 'text');
  assert.deepEqual(clean(request.config.headers), {'X-Lampa-Expected-User': 'Alice', 'If-Match': '"old"', 'X-Lampa-Csrf': '1'});
  request.success(undefined, 204); assert.deepEqual(result, {ok: true, status: 204});
  request.fail(412); assert.deepEqual(result, {ok: false, status: 412});
  p.core.request('PUT', '/api/v1/user-data', {body}, () => {});
  assert.equal(p.requests[2].config.headers['If-Match'], undefined);
  p.faults.ajax = true;
  assert.doesNotThrow(() => p.core.request('GET', '/', {}, (ok, data, xhr) => { result = {ok, status: xhr.status}; }));
  assert.deepEqual(result, {ok: false, status: 0});
});
