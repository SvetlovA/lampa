// Run the actual ES5 add-on with controlled storage and network responses.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {createHash} = require('node:crypto');
const {test} = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../svtlv/account-sync.js'), 'utf8');
const bundle = fs.readFileSync(path.join(__dirname, '../app.min.js'), 'utf8');
// Resolve methods through the public export: Rollup's internal suffixes drift.
function upstreamStorageMethod(method, module = 'Storage') {
  const exports = bundle.match(new RegExp('  var ' + module + ' = \\{([\\s\\S]*?)\\n  \\};'))[1];
  const name = exports.match(new RegExp('\\b' + method + ': ([\\w$]+)'))[1];
  const start = bundle.indexOf('\n  function ' + name + '(') + 1;
  const end = bundle.indexOf('\n  }', start) + 4;
  return '(' + bundle.slice(start, end) + ')';
}
const realScope = JSON.parse(fs.readFileSync(path.join(__dirname, '../svtlv/storage-scope.json'), 'utf8'));
const BASE = 'svtlv_sync_base', USER = 'svtlv_sync_user';
const REVISION = 'svtlv_sync_revision';
const SCOPE = 'svtlv_sync_scope';
const LIMIT = 4 * 1024 * 1024;
const clean = value => JSON.parse(JSON.stringify(value));
// Inspect private core functions without adding a production debug/public API.
const inspected = source.replace(/\}\)\(\);\s*$/, `window.core = {
  request: request, loadScope: loadScope, classify: classify, synced: synced,
  fp: fp, dirty: dirty, readBase: readBase, writeBase: writeBase,
  exportLocal: exportLocal, overlay: overlay, applyDoc: applyDoc,
  clearSynced: clearSynced, fitBody: fitBody,
  bootState: function () { return {ready: ready, lastDoc: lastDoc, lastEtag: lastEtag, scope: !!scope}; },
  accountState: function () { return {uploads: uploads, handledUser: handledUser}; },
  whileApplying: function (fn) { applying = true; try { fn(); } finally { applying = false; } },
  disabled: function () { return disabled; }, failure: function () { return failure; }
}; })();`);

function page(options = {}) {
  const items = options.sharedItems || Object.assign(Object.create(null), options.items);
  const faults = {}, calls = [], requests = [], notices = [], trace = [];
  const listeners = {}, windowEvents = {}, documentEvents = {}, storageListeners = {}, reloads = [], events = [];
  const document = {hidden: !!options.hidden};
  const storageWrites = [], refreshes = [], memory = Object.create(null);
  const timers = new Map();
  let clock = 0, timerID = 0;
  function setTimeout(fn, delay) {
    const id = ++timerID;
    trace.push({timer: delay});
    timers.set(id, {fn, time: clock + delay});
    return id;
  }
  function tick(ms) {
    const end = clock + ms;
    for (;;) {
      const next = [...timers].filter(([, timer]) => timer.time <= end).sort((a, b) => a[1].time - b[1].time)[0];
      if (!next) break;
      timers.delete(next[0]); clock = next[1].time; next[1].fn();
    }
    clock = end;
  }
  function check(method, key, value) {
    calls.push({method, key});
    trace.push({method, key});
    if (faults[method]) throw new Error(method);
    if (method === 'setItem' && faults.quota === key &&
      (faults.quotaValue === undefined || faults.quotaValue === String(value))) {
      const error = new Error('quota'); error.name = 'QuotaExceededError'; throw error;
    }
    if (method === 'getItem' && faults.read === key) throw new Error('read');
  }
  const store = {
    get length() { check('length'); return Object.keys(items).length; },
    key(index) { check('key'); return Object.keys(items)[index] ?? null; },
    getItem(key) { check('getItem', key); return items[key] ?? null; },
    setItem(key, value) { check('setItem', key, value); if (!faults.silent) items[key] = String(value); },
    removeItem(key) { check('removeItem', key); if (!faults.silentRemove) delete items[key]; }
  };
  const window = {
    location: {protocol: options.protocol || 'https:', reload() { reloads.push({...items}); }},
    svtlv_account_state: options.state,
    Lampa: {
      Storage: {
        get: (key, fallback) => key === 'language' && fallback !== '' ? options.language || 'en' :
          items[key] ?? options.reserve?.[key] ?? fallback,
        set(key, value, nolisten, callerror) {
          storageWrites.push({key, value});
          memory[key] = value;
          try { store.setItem(key, value && typeof value === 'object' ? JSON.stringify(value) : value); }
          catch (err) { if (callerror) callerror(err); }
          if (!nolisten) for (const fn of storageListeners.change || []) fn({name: key, value});
        },
        listener: {follow(name, fn) { (storageListeners[name] ||= []).push(fn); }}
      },
      Favorite: {read() { refreshes.push('favorite'); }},
      Timeline: {filename() { return options.timelineKey || 'file_view'; }, read() { refreshes.push('timeline'); }},
      Noty: {show: text => notices.push(text)},
      Listener: {
        follow(name, fn) { (listeners[name] ||= []).push(fn); },
        send(name, event) { events.push({name, event}); for (const fn of listeners[name] || []) fn(event); }
      }
    }
  };
  const Lampa = window.Lampa;
  if (options.upstreamStorage) {
    const upstream = vm.createContext({window, readed: memory, reserve: options.reserve || {},
      Storage: Lampa.Storage, values: {}, defaults: {}, Params: {},
      Arrays: {isObject: value => value !== null && typeof value === 'object' && !Array.isArray(value),
        isArray: Array.isArray},
      listener: {send(name, event) { for (const fn of storageListeners[name] || []) fn(event); }},
      Cache: {rewriteData() { return Promise.resolve(); }}, console: {log() {}}
    });
    Lampa.Storage.get = vm.runInContext(upstreamStorageMethod('get'), upstream);
    // Use the actual selector registration and public field methods for defaults.
    upstream.select = vm.runInContext(upstreamStorageMethod('select', 'Params'), upstream);
    vm.runInContext(bundle.match(/  select\('torrserver_use_link',[\s\S]*?\);/)[0], upstream);
    upstream.Params.field = vm.runInContext(upstreamStorageMethod('field', 'Params'), upstream);
    Lampa.Storage.field = vm.runInContext(upstreamStorageMethod('field'), upstream);
    const set = vm.runInContext(upstreamStorageMethod('set'), upstream);
    Lampa.Storage.set = function (key, value, nolisten, callerror) {
      storageWrites.push({key, value});
      return set(key, value, nolisten, callerror);
    };
  }
  if (options.preboot) delete window.Lampa;
  Object.defineProperty(window, 'localStorage', {
    get() { if (faults.property) throw new Error('localStorage getter'); return store; }
  });
  function $(target) {
    assert.ok(target === window || target === document);
    const handlers = target === window ? windowEvents : documentEvents;
    return {on(name, fn) { (handlers[name] ||= []).push(fn); }};
  }
  $.ajax = function (config) {
    trace.push({url: config.url});
    if (faults.ajax) throw new Error('ajax');
    let etag = '"etag"';
    const xhr = {status: 200, getResponseHeader: name => name === 'ETag' ? etag : null};
    const request = {
      config,
      success(data, status = 200, responseEtag = '"etag"') {
        etag = responseEtag; xhr.status = status; config.success(data, 'success', xhr);
      },
      fail(status = 503, body, textOnly = false) {
        config.error({...xhr, status, responseJSON: textOnly ? undefined : body,
          responseText: typeof body === 'string' ? body : JSON.stringify(body)});
      }
    };
    requests.push(request);
    return request;
  };
  const context = vm.createContext({window, document, $, cache_version: 42, setTimeout, clearTimeout: id => timers.delete(id)});
  vm.runInContext(options.uninspected ? source : inspected, context);
  return {
    window, core: window.core, items, faults, calls, requests, notices, timers, trace, tick, reloads, events,
    storageWrites, refreshes, memory,
    change(key, value) {
      if (value === null) delete items[key]; else items[key] = String(value);
      // A local Storage change comes from a writer that updates/invalidates its
      // own cache too; raw sibling writes are modeled through sharedItems instead.
      if (options.upstreamStorage) {
        if (value === null) delete memory[key]; else memory[key] = value;
      }
      for (const fn of storageListeners.change || []) fn({name: key, value});
    },
    visibility(hidden) {
      document.hidden = hidden;
      for (const fn of documentEvents.visibilitychange || []) fn();
    },
    install() { window.Lampa = Lampa; },
    state(state) {
      window.svtlv_account_state = state;
      Lampa.Listener.send('svtlv_account', {type: 'state', state});
    },
    storageEvent(change, wrapped = true) {
      for (const fn of windowEvents.storage || []) fn(wrapped ? {originalEvent: change} : change);
    },
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

test('both TorrServer URLs must sync while selection stays local and shared credentials stay sealed', () => {
  const data = {torrserver_url: 'http://tailnet:8090', torrserver_url_two: 'http://192.168.1.2:8090',
    torrserver_auth: 'true', torrserver_login: 'user', torrserver_password: 'secret'};
  const p = page({items: {...data, torrserver_use_link: 'two'}}); p.load();
  for (const key of Object.keys(data)) {
    assert.equal(p.core.classify(key), 'user_connection', key);
    assert.equal(p.core.synced(key), true, key);
  }
  assert.equal(p.core.classify('torrserver_use_link'), 'device');
  assert.equal(p.core.synced('torrserver_use_link'), false);
  assert.deepEqual(clean(p.core.exportLocal()), data);
  const body = clean(p.core.fitBody({...data, torrserver_use_link: 'one'}));
  assert.deepEqual(body.data, data);
  for (const key of ['torrserver_login', 'torrserver_password']) assert.ok(body.encrypt.includes(key));
  for (const key of ['torrserver_url', 'torrserver_url_two', 'torrserver_use_link']) {
    assert.equal(body.encrypt.includes(key), false, key);
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

test('overlay exports empty and removed dirty values as tombstones and retains remote tombstones', () => {
  for (const value of ['', null]) {
    const p = page({items: {language: 'en', torrserver_password: 'secret'}}); p.load();
    p.syncedBase({language: 'en', torrserver_password: 'secret'});
    p.change('language', value);
    const data = clean(p.core.overlay({language: 'de', torrserver_password: 'secret', tmdb_lang: ''}));
    assert.deepEqual(data, {language: '', torrserver_password: 'secret', tmdb_lang: ''});
    assert.deepEqual(clean(p.core.fitBody(data).data), data);
    assert.equal(p.core.dirty('language'), true);
    assert.equal(p.core.applyDoc({language: 'fr'}).changed, false, 'polling cannot restore a dirty deletion');
    const q = page({items: {language: 'en', tmdb_lang: 'ru'}}); q.load();
    q.syncedBase({language: 'en', tmdb_lang: 'ru'});
    q.core.applyDoc(data);
    assert.equal(q.items.language, ''); assert.equal(q.items.tmdb_lang, '');
    assert.equal(Object.hasOwn(JSON.parse(q.items[BASE]), 'language'), false);
    assert.equal(q.core.dirty('language'), false);
  }
});

test('missing encrypted fields invalidate only a clean prior base and preserve dirty deletions', () => {
  for (const value of ['secret', 'changed', '', null]) {
    const p = page({items: {torrserver_password: 'secret', file_view: 'progress'}}); p.load();
    p.syncedBase({torrserver_password: 'secret', file_view: 'progress'});
    p.change('torrserver_password', value);
    assert.equal(p.core.applyDoc({}).changed, false);
    assert.equal(p.items.torrserver_password, value === null ? undefined : value);
    assert.equal(p.core.dirty('torrserver_password'), true);
    assert.equal(p.core.dirty('file_view'), false, 'best-effort nonencrypted omissions remain clean');
    assert.deepEqual(clean(p.core.overlay({})), {torrserver_password: value || ''});
    if (value !== 'secret') assert.equal(JSON.parse(p.items[BASE]).torrserver_password, p.core.fp('secret'),
      'dirty values retain their old comparison base, including deletions');
    const restarted = page({items: p.items}); restarted.load();
    assert.equal(restarted.core.dirty('torrserver_password'), true, 'recovery survives reload');
  }
});

test('an explicit remote encrypted tombstone clears its base and never becomes a recovery upload', () => {
  const p = page({items: {torrserver_password: 'secret'}}); p.load();
  p.syncedBase({torrserver_password: 'secret'});
  assert.equal(p.core.applyDoc({torrserver_password: ''}).changed, true);
  assert.equal(p.items.torrserver_password, '');
  assert.equal(Object.hasOwn(JSON.parse(p.items[BASE]), 'torrserver_password'), false);
  assert.equal(p.core.dirty('torrserver_password'), false);
  assert.deepEqual(clean(p.core.overlay({torrserver_password: ''})), {torrserver_password: ''});
  p.core.applyDoc({});
  assert.equal(p.core.dirty('torrserver_password'), false, 'later omission cannot resurrect a cleared credential');
  assert.deepEqual(clean(p.core.overlay({})), {});
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

test('body keeps tombstones, filters disallowed and non-string data, and accepts new allowlist keys', () => {
  const scope = clean(realScope); scope.scopes.user.push('new.setting');
  const p = page(); p.load(scope);
  const body = p.core.fitBody({'new.setting': 'yes', plugins: 'code', language: '', tmdb_lang: null, favorite: {a: 1}});
  assert.deepEqual(clean(body.data), {'new.setting': 'yes', language: ''});
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

function bootPage(items = {}) {
  const p = page({preboot: true, items: {[USER]: 'Alice', ...items}});
  let starts = 0;
  p.bootTraceStart = p.trace.length;
  p.window.svtlv_sync_boot(() => starts++);
  p.starts = () => starts;
  p.scopeRequest = p.requests.find(r => r.config.url.startsWith('svtlv/storage-scope.json'));
  p.dataRequest = p.requests.find(r => r.config.url === '/api/v1/user-data');
  return p;
}

const documentV2 = data => ({schema_version: 2, data});
const missingDocument = {error: {code: 'user_data_not_found', message: 'user data not found'}};

test('unmarked boot starts immediately with no scope or user-data requests, exactly once', () => {
  for (const marker of [undefined, '']) {
    const p = page({preboot: true, items: {language: 'ru', [USER]: marker}, uninspected: true});
    let starts = 0, second = 0;
    const bootTraceStart = p.trace.length;
    p.window.svtlv_sync_boot(() => starts++);
    p.window.svtlv_sync_boot(() => second++);
    p.install();
    p.tick(10000);
    assert.equal(starts, 1); assert.equal(second, 0);
    assert.equal(p.requests.length, 0); assert.equal(p.timers.size, 0);
    assert.equal(p.items.language, 'ru');
    assert.deepEqual(p.trace[bootTraceStart], {timer: 3000}, 'deadline is armed before reading storage');
  }
});

test('marked boot fetches in parallel with identity, applies before starting, and keeps ETag', () => {
  for (const scopeFirst of [false, true]) {
    const p = bootPage({player: 'device'});
    assert.equal(p.requests.length, 2);
    assert.equal(p.starts(), 0);
    assert.deepEqual(p.trace[p.bootTraceStart], {timer: 3000});
    assert.deepEqual(clean(p.dataRequest.config.headers), {'X-Lampa-Expected-User': 'Alice'});
    assert.equal(p.dataRequest.config.timeout, 3000);
    const data = {language: 'ru', favorite: '{"book":[1]}', plugins: '["untrusted"]', tmdb_lang: false};
    const responses = [() => p.scopeRequest.success(clean(realScope)), () => p.dataRequest.success(documentV2(data))];
    if (!scopeFirst) responses.reverse();
    responses[0]();
    assert.equal(p.starts(), 0); assert.equal(p.items.language, undefined);
    responses[1]();
    assert.equal(p.starts(), 1);
    assert.equal(p.items.language, 'ru'); assert.equal(p.items.favorite, data.favorite);
    assert.equal(p.items.player, 'device'); assert.equal(p.items.plugins, undefined); assert.equal(p.items.tmdb_lang, undefined);
    assert.equal(p.items[USER], 'Alice');
    assert.deepEqual(clean(p.core.bootState()), {ready: true, lastDoc: data, lastEtag: '"etag"', scope: true});
    assert.equal(JSON.parse(p.items[BASE]).language, p.core.fp('ru'));
    p.install(); p.tick(5000);
    assert.equal(p.starts(), 1); assert.equal(p.timers.size, 0); assert.equal(p.notices.length, 0);
  }
});

test('a nolisten language edit survives boot and remains dirty against its persisted base', () => {
  const seed = page(); seed.syncedBase({language: 'en'});
  const p = bootPage({language: 'ru', [BASE]: seed.items[BASE]});
  p.dataRequest.success(documentV2({language: 'en', tmdb_lang: 'en'}));
  p.scopeRequest.success(clean(realScope));
  assert.equal(p.items.language, 'ru'); assert.equal(p.items.tmdb_lang, 'en');
  assert.equal(p.core.dirty('language'), true); assert.equal(p.core.dirty('tmdb_lang'), false);
  assert.equal(JSON.parse(p.items[BASE]).language, p.core.fp('en'));
  assert.equal(p.core.bootState().ready, true); assert.equal(p.starts(), 1);
});

test('404 is ready only for user_data_not_found and preserves local data and base', () => {
  for (const textOnly of [false, true]) {
    const p = bootPage({language: 'ru', [BASE]: '{}'});
    p.scopeRequest.success(clean(realScope));
    p.dataRequest.fail(404, missingDocument, textOnly);
    assert.equal(p.core.bootState().ready, true);
    assert.deepEqual(clean(p.core.bootState().lastDoc), {}); assert.equal(p.core.bootState().lastEtag, '');
    assert.equal(p.items.language, 'ru'); assert.equal(p.items[BASE], '{}');
    assert.equal(p.starts(), 1); assert.equal(p.core.failure(), '');
  }
});

test('401 and 409 clear synced values, mask reserve copies, and remove marker/base before start', () => {
  for (const status of [401, 409]) for (const scopeFirst of [false, true]) {
    const p = bootPage({favorite: '{"book":[1]}', torrserver_password: 'secret', player: 'device', [BASE]: '{}'});
    const replies = [() => p.scopeRequest.success(clean(realScope)), () => p.dataRequest.fail(status)];
    if (!scopeFirst) replies.reverse();
    replies[0](); assert.equal(p.starts(), 0);
    replies[1]();
    for (const key of ['favorite', 'torrserver_password', 'file_view']) assert.equal(p.items[key], '');
    assert.equal(p.items.player, 'device'); assert.equal(p.items[USER], undefined); assert.equal(p.items[BASE], undefined);
    assert.equal(p.core.bootState().ready, false); assert.equal(p.core.disabled(), false);
    assert.equal(p.starts(), 1); assert.equal(p.core.failure(), '');
  }
});

test('invalid documents and all other API failures preserve local data and defer one notice', () => {
  const cases = [
    r => r.success(null), r => r.success([]), r => r.success({schema_version: 1, data: {language: 'en'}}),
    r => r.success({schema_version: 2, data: []}), r => r.success({schema_version: 2}),
    r => r.success({schema_version: 2, data: null}), r => r.success(documentV2({language: 'en'}), 204),
    r => r.success(undefined, 304), r => r.fail(0), r => r.fail(500), r => r.fail(503),
    r => r.fail(400), r => r.fail(403), r => r.fail(413), r => r.fail(415),
    r => r.fail(404, {error: {code: 'not_found'}}), r => r.fail(404, '<html>'),
    r => r.fail(404)
  ];
  for (const reply of cases) {
    const p = bootPage({language: 'ru', [BASE]: '{}'});
    p.scopeRequest.success(clean(realScope)); reply(p.dataRequest);
    assert.equal(p.starts(), 1); assert.equal(p.items.language, 'ru'); assert.equal(p.items[USER], 'Alice');
    assert.equal(p.items[BASE], '{}'); assert.equal(p.core.bootState().ready, false);
    assert.equal(p.core.disabled(), false); assert.equal(p.core.failure(), 'save');
    assert.deepEqual(p.notices, []);
    p.tick(100); assert.deepEqual(p.notices, []);
    p.install(); p.tick(100);
    assert.deepEqual(p.notices, ['Not saved to Account']);
    p.tick(5000); assert.equal(p.notices.length, 1); assert.equal(p.starts(), 1);
  }
});

test('scope failure and invalid scope do not apply a valid document or block boot', () => {
  for (const invalid of [false, true]) {
    const p = bootPage({language: 'ru'});
    p.dataRequest.success(documentV2({language: 'en', favorite: '{}'}));
    if (invalid) p.scopeRequest.success({}); else p.scopeRequest.fail(503);
    assert.equal(p.starts(), 1); assert.equal(p.items.language, 'ru'); assert.equal(p.items.favorite, undefined);
    assert.equal(p.core.bootState().ready, false); assert.equal(p.core.disabled(), invalid);
    assert.equal(p.core.failure(), 'save');
    p.install(); p.tick(0); assert.deepEqual(p.notices, ['Not saved to Account']);
  }
});

test('three-second deadline starts Lampa once; late data and late scope responses do nothing', () => {
  for (const first of ['scope', 'data', 'none']) for (const invalidLateScope of [false, true]) {
    const p = bootPage({language: 'ru', [BASE]: '{}'});
    if (first === 'scope') p.scopeRequest.success(clean(realScope));
    if (first === 'data') p.dataRequest.success(documentV2({tmdb_lang: 'en'}));
    p.tick(2999); assert.equal(p.starts(), 0);
    p.tick(1); assert.equal(p.starts(), 1); assert.equal(p.core.bootState().ready, false);
    const state = clean(p.core.bootState()), items = {...p.items};
    p.scopeRequest.success(invalidLateScope ? {} : clean(realScope));
    p.scopeRequest.fail(500);
    p.dataRequest.success(documentV2({language: 'en', tmdb_lang: 'en'}));
    p.dataRequest.fail(401);
    assert.deepEqual(clean(p.items), items); assert.deepEqual(clean(p.core.bootState()), state);
    assert.equal(p.core.disabled(), false, 'invalid late scope must not disable future recovery');
    p.install(); p.tick(10000);
    assert.equal(p.starts(), 1); assert.deepEqual(p.notices, ['Not saved to Account']);
  }
});

test('duplicate boot calls and duplicate responses never restart or reapply', () => {
  const p = bootPage(); let second = 0;
  p.window.svtlv_sync_boot(() => second++);
  assert.equal(p.requests.length, 2);
  p.scopeRequest.success(clean(realScope));
  p.dataRequest.success(documentV2({language: 'en'}));
  p.items.language = 'ru';
  p.scopeRequest.success({}); p.dataRequest.success(documentV2({language: 'de'})); p.dataRequest.fail(401);
  p.window.svtlv_sync_boot(() => second++); p.tick(10000);
  assert.equal(p.items.language, 'ru'); assert.equal(p.items[USER], 'Alice');
  assert.equal(p.starts(), 1); assert.equal(second, 0); assert.equal(p.core.disabled(), false);
});

test('must-sync storage failure disables uploads and still starts with a deferred notice', () => {
  const p = bootPage(); p.faults.quota = 'language';
  p.scopeRequest.success(clean(realScope)); p.dataRequest.success(documentV2({language: 'ru'}));
  assert.equal(p.starts(), 1); assert.equal(p.core.bootState().ready, false); assert.equal(p.core.disabled(), true);
  assert.equal(p.core.failure(), 'storage'); assert.deepEqual(p.notices, []);
  p.install(); p.tick(0); assert.deepEqual(p.notices, ['Account sync is unavailable on this device']);
  assert.equal(p.core.fitBody({language: 'ru'}), null);
});

test('optional quota failure skips that value but boot is ready', () => {
  const p = bootPage(); p.faults.quota = 'file_view';
  p.scopeRequest.success(clean(realScope));
  p.dataRequest.success(documentV2({file_view: '{}', language: 'ru'}));
  assert.equal(p.items.file_view, undefined); assert.equal(p.items.language, 'ru');
  assert.equal(p.core.bootState().ready, true); assert.equal(p.core.disabled(), false); assert.equal(p.starts(), 1);
});

test('storage access and synchronous ajax exceptions cannot hold boot', () => {
  for (const fault of ['property', 'getItem', 'ajax']) {
    const p = page({preboot: true, items: {[USER]: 'Alice'}}); p.faults[fault] = true;
    let starts = 0;
    assert.doesNotThrow(() => p.window.svtlv_sync_boot(() => starts++));
    assert.equal(starts, 1); assert.equal(p.core.bootState().ready, false);
    assert.equal(p.core.disabled(), fault !== 'ajax');
    p.install(); p.tick(0); assert.equal(p.notices.length, 1);
    p.tick(5000); assert.equal(starts, 1);
  }
});

const signedIn = user_id => ({status: 'signed_in', user_id});
const guestState = {status: 'guest', user_id: ''};
function loginPage(options = {}, user = 'Alice') {
  const p = page(options);
  p.state(signedIn(user));
  const scopeRequest = p.requests.find(r => r.config.url.startsWith('svtlv/storage-scope.json'));
  if (scopeRequest) scopeRequest.success(clean(realScope));
  p.dataRequest = p.requests.find(r => r.config.url === '/api/v1/user-data');
  return p;
}

test('polls for Lampa, reads the latest account snapshot, and installs listeners only once', () => {
  const p = page({preboot: true, state: signedIn('Old')});
  p.tick(300); assert.equal(p.requests.length, 0);
  p.window.svtlv_account_state = signedIn('Alice');
  p.install(); p.tick(100);
  assert.equal(p.requests.length, 1);
  p.requests[0].success(clean(realScope));
  assert.equal(p.requests.length, 2);
  assert.equal(p.requests[1].config.headers['X-Lampa-Expected-User'], 'Alice');
  p.requests[1].success(documentV2({}));
  p.reload(); p.tick(1000); p.state(signedIn('Alice'));
  assert.equal(p.requests.length, 2);
  assert.equal(p.core.accountState().uploads, true);
});

test('TV sign-in replaces guest data and persists marker/base before its single reload', () => {
  const p = loginPage({items: {favorite: '{"guest":1}', language: 'ru', player: 'native', plugins: 'code'}});
  assert.equal(p.dataRequest.config.headers['X-Lampa-Expected-User'], 'Alice');
  p.dataRequest.success(documentV2({favorite: '{"server":2}', language: 'en', plugins: 'remote'}));
  assert.equal(p.reloads.length, 1);
  assert.equal(p.reloads[0][USER], 'Alice');
  assert.equal(JSON.parse(p.reloads[0][BASE]).favorite, p.core.fp('{"server":2}'));
  assert.equal(p.items.favorite, '{"server":2}'); assert.equal(p.items.language, 'en');
  assert.equal(p.items.player, 'native'); assert.equal(p.items.plugins, 'code');
  assert.equal(p.core.dirty('favorite'), false);
  p.state(signedIn('Alice')); p.dataRequest.success(documentV2({language: 'de'}));
  assert.equal(p.reloads.length, 1); assert.equal(p.items.language, 'en');
});

test('equal TV sign-in data enables uploads without reloading; repeated signed-in is ignored', () => {
  const data = {favorite: '{"book":[1]}', language: 'en'};
  const p = loginPage({items: data});
  p.state(signedIn('Alice'));
  assert.equal(p.requests.length, 2, 'duplicate pending state does not request twice');
  p.dataRequest.success(documentV2(data));
  assert.equal(p.reloads.length, 0);
  assert.deepEqual(clean(p.core.accountState()), {uploads: true, handledUser: 'Alice'});
  assert.equal(p.items[USER], 'Alice'); assert.equal(p.core.dirty('favorite'), false);
  const before = {...p.items}, calls = p.calls.length;
  p.state(signedIn('Alice')); p.state({status: 'checking'}); p.state({status: 'unavailable'}); p.state(signedIn('Alice'));
  assert.equal(p.requests.length, 2); assert.equal(p.calls.length, calls);
  assert.deepEqual({...p.items}, before); assert.equal(p.core.accountState().uploads, true);
});

test('a server document without favorite clears guest favorite and reloads', () => {
  const p = loginPage({items: {favorite: '{"guest":1}'}});
  p.dataRequest.success(documentV2({}));
  assert.equal(p.items.favorite, ''); assert.equal(p.reloads.length, 1);
  assert.equal(p.reloads[0][USER], 'Alice'); assert.deepEqual(JSON.parse(p.items[BASE]), {});
});

test('a server document clears reserve-only favorites before reloading', () => {
  const p = loginPage({reserve: {favorite: {book: [1]}}});
  p.dataRequest.success(documentV2({}));
  assert.equal(p.items.favorite, ''); assert.equal(p.reloads.length, 1);
});

test('a different account replaces previous synced data instead of preserving it as dirty', () => {
  const p = loginPage({items: {[USER]: 'Bob', favorite: 'old private data', torrserver_password: 'old secret',
    language: 'ru', player: 'native'}});
  p.dataRequest.success(documentV2({language: 'en'}));
  assert.equal(p.items.favorite, ''); assert.equal(p.items.torrserver_password, '');
  assert.equal(p.items.language, 'en'); assert.equal(p.items.player, 'native');
  assert.equal(p.reloads[0][USER], 'Alice'); assert.equal(p.core.dirty('language'), false);
});

test('partial account imports retain ownership for logout and account replacement cleanup', () => {
  for (const priorOwner of ['', 'Previous']) for (const failure of [BASE, 'torrserver_password']) {
    for (const restart of [false, true]) for (const next of [guestState, signedIn('Bob')]) {
      const p = loginPage({items: {plugins: 'device', ...(priorOwner ? {[USER]: priorOwner} : {})}});
      p.faults.quota = failure;
      if (failure === 'torrserver_password') p.faults.quotaValue = 'Alice secret';
      p.dataRequest.success(documentV2({favorite: '{"book":["Alice private"]}', torrserver_password: 'Alice secret'}));
      assert.equal(p.core.disabled(), true);
      assert.equal(p.items.favorite, '{"book":["Alice private"]}');
      assert.equal(p.items[USER], 'Alice', 'private imported values always have a persisted owner');
      delete p.faults.quota;
      const q = restart ? page({items: p.items}) : p;
      q.state(next);
      if (restart) {
        q.requests[0].success(clean(realScope));
        if (next.status === 'signed_in') q.requests.at(-1).fail(404, missingDocument);
      }
      assert.equal(q.items.favorite, ''); assert.equal(q.items.torrserver_password, '');
      assert.equal(q.items.plugins, 'device');
      assert.equal(q.reloads.length, 1);
      const fresh = page({items: q.items, upstreamStorage: true,
        reserve: {favorite: '{"book":["Alice private"]}', torrserver_password: 'Alice secret'}});
      assert.equal(fresh.window.Lampa.Storage.get('favorite', ''), '');
      assert.equal(fresh.window.Lampa.Storage.get('torrserver_password', ''), '');
      assert.equal(q.requests.filter(r => r.config.type === 'PUT').length, 0);
    }
  }
});

test('an ownership write failure aborts account import before any private value is written', () => {
  for (const priorOwner of ['', 'Previous']) {
    const p = loginPage({items: priorOwner ? {[USER]: priorOwner, favorite: 'old private'} : {}});
    p.faults.quota = USER;
    p.dataRequest.success(documentV2({favorite: 'new private', torrserver_password: 'new secret'}));
    assert.equal(p.core.disabled(), true);
    assert.equal(p.items[USER], undefined);
    assert.equal(p.items.favorite, ''); assert.equal(p.items.torrserver_password, '');
    assert.equal(p.calls.some(call => call.method === 'setItem' && call.key === BASE), false,
      'no imported fingerprints are written without an owner');
    assert.equal(p.core.accountState().uploads, false);
  }
});

test('404 on first login retains guest data, resets base, and enables its upload', () => {
  const p = loginPage({items: {favorite: '{"guest":1}', language: 'ru', [BASE]: '{"language":"2:123"}'}});
  p.dataRequest.fail(404, missingDocument);
  assert.equal(p.reloads.length, 0); assert.equal(p.items[USER], 'Alice');
  assert.deepEqual(JSON.parse(p.items[BASE]), {});
  assert.equal(p.core.accountState().uploads, true);
  assert.equal(p.core.dirty('favorite'), true); assert.equal(p.core.dirty('language'), true);
  assert.deepEqual(clean(p.core.fitBody(p.core.overlay(p.core.bootState().lastDoc)).data),
    {favorite: '{"guest":1}', language: 'ru'});
  assert.equal(p.core.bootState().lastEtag, '');
});

test('404 for a different account clears its data and persists the new empty base/marker before reload', () => {
  const p = loginPage({items: {[USER]: 'Bob', favorite: 'private', language: 'ru', plugins: 'local'}});
  p.dataRequest.fail(404, missingDocument);
  assert.equal(p.items.favorite, ''); assert.equal(p.items.language, ''); assert.equal(p.items.plugins, 'local');
  assert.equal(p.reloads.length, 1); assert.equal(p.reloads[0][USER], 'Alice');
  assert.deepEqual(JSON.parse(p.reloads[0][BASE]), {});
  assert.deepEqual(clean(p.core.exportLocal()), {});
});

test('same-account recovery preserves dirty edits and reloads only if server apply changes a value', () => {
  for (const changed of [false, true]) {
    const p = page({items: {[USER]: 'Alice', language: 'ru', favorite: 'same'}});
    p.syncedBase({language: 'en', favorite: 'same'});
    p.state(signedIn('Alice')); p.requests[0].success(clean(realScope));
    p.requests[1].success(documentV2({language: 'en', favorite: changed ? 'new' : 'same'}));
    assert.equal(p.items.language, 'ru'); assert.equal(p.core.dirty('language'), true);
    assert.equal(p.items.favorite, changed ? 'new' : 'same');
    assert.equal(p.reloads.length, changed ? 1 : 0);
    assert.equal(p.core.accountState().uploads, !changed);
    assert.equal(p.items[USER], 'Alice');
  }
});

test('successful pre-boot data enables same-account uploads without a second request or reload', () => {
  const p = bootPage();
  p.scopeRequest.success(clean(realScope)); p.dataRequest.success(documentV2({language: 'en'}));
  p.window.svtlv_account_state = signedIn('Alice'); p.install(); p.tick(100);
  assert.equal(p.requests.length, 2); assert.equal(p.reloads.length, 0);
  assert.equal(p.core.accountState().uploads, true);
});

test('boot-to-session sibling saves refresh upstream caches before the first local edit', () => {
  const server = sharedDocument({favorite: '{"book":[1]}', file_view: '{"movie":0.1}'});
  const sharedItems = {[USER]: 'Alice'};
  const p = page({preboot: true, sharedItems, upstreamStorage: true, uninspected: true});
  p.window.svtlv_sync_boot(() => p.install());
  p.requests[0].success(clean(realScope)); server.respond(p.requests[1]);
  p.tick(100);
  p.window.Lampa.Storage.get('favorite'); p.window.Lampa.Storage.get('file_view');
  const sibling = acceptanceDevice(server, {sharedItems});
  sibling.window.Lampa.Storage.set('favorite', {book: [1, 2]});
  sibling.window.Lampa.Storage.set('file_view', {movie: 0.5});
  sibling.tick(2000); server.respond(sibling.requests.at(-1)); server.respond(sibling.requests.at(-1));
  for (const key of ['favorite', 'file_view']) p.storageEvent({key, newValue: sharedItems[key]});
  assert.deepEqual(clean(p.window.Lampa.Storage.get('favorite')), {book: [1]}, 'session is still pending');
  p.state(signedIn('Alice'));
  const favorite = p.window.Lampa.Storage.get('favorite');
  assert.deepEqual(clean(favorite), {book: [1, 2]});
  assert.deepEqual(clean(p.window.Lampa.Storage.get('file_view')), {movie: 0.5});
  assert.ok(p.refreshes.includes('favorite')); assert.ok(p.refreshes.includes('timeline'));
  favorite.book.push(3); p.window.Lampa.Storage.set('favorite', favorite);
  p.tick(2000); server.respond(p.requests.at(-1)); server.respond(p.requests.at(-1));
  assert.deepEqual(JSON.parse(server.data().favorite), {book: [1, 2, 3]});
  assert.equal(p.reloads.length, 0);
});

test('boot discards another account response even when its shared baseline is identical', () => {
  for (const scopeFirst of [false, true]) for (const status of [200, 401, 409, 404]) {
    const p = bootPage({language: 'en', favorite: ''});
    p.syncedBase({language: 'en'});
    // Start a fresh request so its baseline matches both accounts throughout the switch.
    const q = bootPage({...p.items});
    const reply = () => status === 200 ? q.dataRequest.success(documentV2({language: 'en', favorite: 'Alice private'})) :
      q.dataRequest.fail(status, status === 404 ? missingDocument : undefined);
    if (scopeFirst) q.scopeRequest.success(clean(realScope));
    else reply();
    q.items[USER] = 'Bob';
    const bobItems = {...q.items};
    if (scopeFirst) reply();
    else q.scopeRequest.success(clean(realScope));
    assert.equal(q.starts(), 1);
    assert.deepEqual({...q.items, [SCOPE]: undefined}, {...bobItems, [SCOPE]: undefined},
      'an obsolete success or auth failure cannot change Bob data');
    assert.equal(q.core.bootState().ready, false);
    q.window.svtlv_account_state = signedIn('Bob'); q.install(); q.tick(100);
    assert.equal(q.requests.length, 3, 'Bob must fetch his own document before enabling uploads');
    assert.equal(q.requests.at(-1).config.headers['X-Lampa-Expected-User'], 'Bob');
    assert.equal(q.core.accountState().uploads, false);
    q.requests.at(-1).success(documentV2({language: 'en', favorite: ''}));
    assert.equal(q.core.accountState().uploads, true);
    assert.equal(q.items.favorite, ''); assert.equal(q.items[USER], 'Bob');
  }
});

test('boot readiness belongs to the fetched account before account listeners attach', () => {
  const p = bootPage({language: 'en'});
  p.scopeRequest.success(clean(realScope));
  p.dataRequest.success(documentV2({language: 'en', favorite: 'Alice private'}));
  assert.equal(p.core.bootState().ready, true);
  p.items[USER] = 'Bob'; p.items.favorite = ''; p.syncedBase({language: 'en'});
  p.window.svtlv_account_state = signedIn('Bob'); p.install(); p.tick(100);
  assert.equal(p.requests.length, 3, 'Alice readiness cannot stand in for Bob document');
  assert.equal(p.requests.at(-1).config.headers['X-Lampa-Expected-User'], 'Bob');
  assert.equal(p.core.accountState().uploads, false);
  p.requests.at(-1).success(documentV2({language: 'en', favorite: ''}));
  assert.equal(p.core.accountState().uploads, true);
  assert.equal(p.items.favorite, ''); assert.deepEqual(clean(p.core.bootState().lastDoc), {language: 'en', favorite: ''});
});

test('account recovery rejects a response after the shared owner changes before its storage event', () => {
  const p = loginPage({items: {[USER]: 'Alice', language: 'en', [BASE]: '{}'}});
  p.items[USER] = 'Bob'; p.items.favorite = '';
  const bobItems = {...p.items};
  p.dataRequest.success(documentV2({language: 'en', favorite: 'Alice private'}));
  assert.deepEqual({...p.items}, bobItems);
  assert.equal(p.core.accountState().uploads, false);
  p.storageEvent({key: USER, oldValue: 'Alice', newValue: 'Bob'});
  assert.equal(p.reloads.length, 1);
  assert.equal(p.requests.filter(r => r.config.type === 'PUT').length, 0);
});

test('404 recovery for the same account preserves its local data and dirty base', () => {
  const p = page({items: {[USER]: 'Alice', language: 'ru'}});
  p.syncedBase({language: 'en'}); const before = p.items[BASE];
  p.state(signedIn('Alice')); p.requests[0].success(clean(realScope)); p.requests[1].fail(404, missingDocument);
  assert.equal(p.items.language, 'ru'); assert.equal(p.items[BASE], before);
  assert.equal(p.core.dirty('language'), true); assert.equal(p.core.accountState().uploads, true);
  assert.equal(p.reloads.length, 0);
});

for (const leaving of [false, true]) {
  test('logout clears synced data and ' + (leaving ? 'lets SSO navigate' : 'reloads the TV'), () => {
    const p = loginPage({items: {language: 'en', favorite: 'book', player: 'native', file_view_123: 'cub'}});
    p.dataRequest.success(documentV2({language: 'en', favorite: 'book'}));
    assert.equal(p.reloads.length, 0);
    p.window.svtlv_account_leaving = leaving;
    p.state(guestState);
    assert.equal(p.items.favorite, ''); assert.equal(p.items.language, ''); assert.equal(p.items.file_view, '');
    assert.equal(p.items.player, 'native'); assert.equal(p.items.file_view_123, 'cub');
    assert.equal(p.items[USER], undefined); assert.equal(p.items[BASE], undefined);
    assert.equal(p.core.accountState().uploads, false);
    assert.equal(p.reloads.length, leaving ? 0 : 1);
    if (!leaving) assert.equal(p.reloads[0][USER], undefined);
    p.state(guestState); assert.equal(p.reloads.length, leaving ? 0 : 1);
  });
}

test('confirmed guest at startup clears marked data after loading scope without a user-data request', () => {
  const p = page({items: {[USER]: 'Alice', favorite: 'private'}, state: guestState});
  assert.equal(p.requests.length, 1); assert.match(p.requests[0].config.url, /^svtlv\/storage-scope/);
  p.requests[0].success(clean(realScope));
  assert.equal(p.requests.length, 1); assert.equal(p.items.favorite, ''); assert.equal(p.items[USER], undefined);
  assert.equal(p.reloads.length, 1);
});

test('confirmed logout and account replacement use the retained cleanup policy after scope failure', () => {
  const policy = clean(realScope);
  policy.scopes.user.push('private_*');
  for (const invalid of [false, true]) for (const next of [guestState, signedIn('Bob')]) {
    const saved = page({items: {[USER]: 'Alice', favorite: 'private', torrserver_password: 'secret',
      private_unsaved: 'unsaved', player: 'native', plugins: 'code', file_view_123: 'cub'}});
    saved.load(policy); saved.syncedBase({favorite: 'private', torrserver_password: 'secret'});
    assert.deepEqual(JSON.parse(saved.items[SCOPE]), {version: 1, scopes: policy.scopes});
    const p = page({items: saved.items, state: next});
    assert.equal(p.requests.length, 1);
    if (invalid) p.requests[0].success({}); else p.requests[0].fail(503);
    for (const key of ['favorite', 'torrserver_password', 'private_unsaved', 'file_view']) {
      assert.equal(p.items[key], '', key + ' is cleared or masks reserve');
    }
    assert.equal(p.items[USER], undefined); assert.equal(p.items[BASE], undefined);
    assert.equal(p.items.player, 'native'); assert.equal(p.items.plugins, 'code');
    assert.equal(p.items.file_view_123, 'cub'); assert.equal(p.reloads.length, 1);
    p.tick(600000); assert.equal(p.requests.length, 1, 'a cached policy never enables data requests');
  }
});

test('boot identity failures clear owned data even when scope validation or fetching fails', () => {
  for (const invalid of [false, true]) for (const status of [401, 409]) for (const scopeFirst of [false, true]) {
    const saved = page({items: {[USER]: 'Alice', favorite: 'private', torrserver_password: 'secret', player: 'native'}});
    saved.load(); saved.syncedBase({favorite: 'private', torrserver_password: 'secret'});
    const p = bootPage(saved.items);
    const failScope = () => invalid ? p.scopeRequest.success({}) : p.scopeRequest.fail(503);
    if (scopeFirst) failScope();
    p.dataRequest.fail(status);
    if (!scopeFirst) failScope();
    assert.equal(p.starts(), 1);
    assert.equal(p.items.favorite, ''); assert.equal(p.items.torrserver_password, '');
    assert.equal(p.items.file_view, ''); assert.equal(p.items.player, 'native');
    assert.equal(p.items[USER], undefined); assert.equal(p.items[BASE], undefined);
    assert.equal(p.core.bootState().ready, false);
  }
});

test('cleanup without a cached scope still clears the legacy owned fingerprint inventory', () => {
  const saved = page({items: {[USER]: 'Alice', favorite: 'private', torrserver_password: 'secret', plugins: 'code'}});
  saved.syncedBase({favorite: 'private', torrserver_password: 'secret'});
  const p = page({items: saved.items, state: guestState});
  p.requests[0].success({});
  assert.equal(p.items.favorite, ''); assert.equal(p.items.torrserver_password, '');
  assert.equal(p.items.plugins, 'code'); assert.equal(p.items[USER], undefined);
  assert.equal(p.reloads.length, 1);
});

test('cached cleanup policy does not apply data or clear an unconfirmed account during an outage', () => {
  for (const invalid of [false, true]) {
    const saved = page({items: {[USER]: 'Alice', favorite: 'private'}});
    saved.load(); saved.syncedBase({favorite: 'private'});
    const p = bootPage(saved.items);
    if (invalid) p.scopeRequest.success({}); else p.scopeRequest.fail(503);
    p.dataRequest.success(documentV2({favorite: 'remote'}));
    p.install(); p.tick(100); p.state({status: 'unavailable'});
    assert.equal(p.items.favorite, 'private'); assert.equal(p.items[USER], 'Alice');
    assert.equal(p.reloads.length, 0); assert.equal(p.core.accountState().uploads, false);
    assert.equal(p.core.bootState().ready, false);
  }
});

test('failed cleanup-policy persistence prevents importing new account data', () => {
  const p = page({items: {favorite: 'guest'}});
  p.state(signedIn('Alice')); p.faults.quota = SCOPE;
  p.requests[0].success(clean(realScope));
  assert.equal(p.core.disabled(), true); assert.equal(p.core.accountState().uploads, false);
  assert.equal(p.requests.length, 1); assert.equal(p.items.favorite, 'guest');
  assert.equal(p.items[USER], undefined);
});

test('scope failure cannot let a delayed boot identity failure clear another account', () => {
  for (const invalid of [false, true]) {
    const saved = page({items: {[USER]: 'Alice', favorite: 'private'}});
    saved.load(); saved.syncedBase({favorite: 'private'});
    const p = bootPage(saved.items);
    if (invalid) p.scopeRequest.success({}); else p.scopeRequest.fail(503);
    p.items[USER] = 'Bob'; p.items.favorite = 'Bob private';
    const before = {...p.items};
    p.dataRequest.fail(401);
    assert.deepEqual({...p.items}, before);
    assert.equal(p.starts(), 1);
  }
});

test('anonymous startup and checking/unavailable states preserve local data and make no requests', () => {
  const p = page({state: guestState, items: {favorite: 'guest'}});
  p.state({status: 'checking'}); p.state({status: 'unavailable'}); p.state(guestState); p.tick(120000);
  assert.equal(p.items.favorite, 'guest'); assert.equal(p.requests.length, 0); assert.equal(p.reloads.length, 0);
  const marked = page({state: {status: 'checking'}, items: {[USER]: 'Alice', favorite: 'book'}});
  marked.state({status: 'unavailable'});
  assert.equal(marked.items.favorite, 'book'); assert.equal(marked.requests.length, 0);
});

test('another tab changing the marker reloads once, while other storage events do nothing', () => {
  for (const wrapped of [false, true]) {
    const p = page();
    p.storageEvent({key: 'favorite', oldValue: 'a', newValue: 'b'}, wrapped);
    p.storageEvent({key: USER, oldValue: 'Alice', newValue: 'Alice'}, wrapped);
    assert.equal(p.reloads.length, 0);
    p.storageEvent({key: USER, oldValue: 'Alice', newValue: null}, wrapped);
    p.storageEvent({key: USER, oldValue: null, newValue: 'Bob'}, wrapped);
    assert.equal(p.reloads.length, 1); assert.equal(p.core.accountState().uploads, false);
  }
});

test('late data responses cannot apply after guest or another signed-in account', () => {
  for (const next of [guestState, signedIn('Bob')]) {
    const p = loginPage(); const old = p.dataRequest;
    p.state(next);
    old.success(documentV2({favorite: 'Alice private data'})); old.fail(401);
    assert.equal(p.items.favorite, undefined); assert.equal(p.items[USER], undefined);
    assert.equal(p.events.filter(e => e.event.type === 'expired').length, 0);
    if (next.user_id) {
      p.requests.at(-1).fail(404, missingDocument);
      assert.equal(p.items[USER], 'Bob'); assert.equal(p.core.accountState().uploads, true);
    }
  }
});

test('late scope callbacks from an obsolete identity cannot disable the current sync', () => {
  const p = page(); p.state(signedIn('Alice')); const old = p.requests[0];
  p.state(signedIn('Bob')); const current = p.requests[1];
  old.success({}); assert.equal(p.core.disabled(), false); assert.equal(p.requests.length, 2);
  current.success(clean(realScope)); p.requests[2].success(documentV2({}));
  assert.equal(p.items[USER], 'Bob'); assert.equal(p.core.accountState().uploads, true);
});

test('account fetch failures keep local data and allow a later signed-in event to retry', () => {
  for (const response of ['network', 'invalid', 'wrong404']) {
    const p = loginPage({items: {favorite: 'guest'}});
    if (response === 'network') p.dataRequest.fail(503);
    else if (response === 'invalid') p.dataRequest.success({schema_version: 1, data: {}});
    else p.dataRequest.fail(404, {error: {code: 'other'}});
    assert.equal(p.items.favorite, 'guest'); assert.equal(p.items[USER], undefined);
    assert.equal(p.core.accountState().uploads, false); assert.equal(p.reloads.length, 0);
    assert.deepEqual(p.notices, ['Not saved to Account']);
    p.state(signedIn('Alice')); assert.equal(p.requests.length, 3);
    p.requests[2].fail(404, missingDocument);
    assert.equal(p.core.accountState().uploads, true);
  }
});

test('post-boot scope fetch failure is safe; invalid scope disables the page', () => {
  for (const invalid of [false, true]) {
    const p = page({items: {favorite: 'guest'}}); p.state(signedIn('Alice'));
    if (invalid) p.requests[0].success({}); else p.requests[0].fail(503);
    assert.equal(p.items.favorite, 'guest'); assert.equal(p.requests.length, 1);
    assert.equal(p.core.accountState().uploads, false); assert.equal(p.core.disabled(), invalid);
    assert.equal(p.reloads.length, 0);
  }
});

test('identity failures request a session recheck without applying the response', () => {
  for (const status of [401, 409]) {
    const p = loginPage({items: {favorite: 'guest'}}); p.dataRequest.fail(status);
    assert.equal(p.items.favorite, 'guest'); assert.equal(p.core.accountState().uploads, false);
    assert.equal(p.events.filter(e => e.event.type === 'expired').length, 1);
    assert.equal(p.reloads.length, 0);
  }
});

test('post-boot storage failure never escapes the account listener or enables uploads', () => {
  const p = loginPage(); p.faults.quota = 'language';
  assert.doesNotThrow(() => p.dataRequest.success(documentV2({language: 'en'})));
  assert.equal(p.core.disabled(), true); assert.equal(p.core.accountState().uploads, false);
  assert.equal(p.items[USER], undefined); assert.equal(p.reloads.length, 0);
});

// Save-only tests stay hidden to isolate save timers from periodic polling.
function savingPage(data = {language: 'en', favorite: '{"book":[1]}'}, options = {hidden: true}) {
  const p = loginPage({...options, items: data});
  p.dataRequest.success(documentV2(data), 200, '"initial"');
  assert.equal(p.reloads.length, 0);
  p.initialRequests = p.requests.length;
  // These fixtures measure save/poll effects after the login cache refresh.
  p.storageWrites.length = 0;
  p.refreshes.length = 0;
  return p;
}
const putBody = request => JSON.parse(request.config.data);
const expireCount = p => p.events.filter(e => e.event.type === 'expired').length;

function startSave(p, key = 'language', value = 'ru') {
  p.change(key, value);
  p.tick(key === 'file_view' ? 300000 : 2000);
  assert.equal(p.requests.at(-1).config.type, 'GET');
  return p.requests.at(-1);
}

test('several synced changes batch into one conditional GET and PUT containing server plus dirty keys', () => {
  const p = savingPage({language: 'en', tmdb_lang: 'en', favorite: '{"old":1}'});
  p.change('language', 'ru'); p.tick(500);
  p.change('language', 'de'); p.change('torrserver_password', 'secret');
  p.tick(1499); assert.equal(p.requests.length, p.initialRequests);
  p.tick(1);
  const get = p.requests.at(-1);
  assert.equal(get.config.type, 'GET');
  assert.deepEqual(clean(get.config.headers), {'X-Lampa-Expected-User': 'Alice', 'If-None-Match': '"initial"'});
  get.success(documentV2({language: 'fr', tmdb_lang: 'fr', favorite: '{"new":2}', plugins: 'remote'}), 200, '"latest"');
  const put = p.requests.at(-1);
  assert.equal(put.config.type, 'PUT');
  assert.equal(put.config.contentType, 'application/json; charset=UTF-8');
  assert.equal(put.config.processData, false); assert.equal(put.config.timeout, 15000);
  assert.deepEqual(clean(put.config.headers), {
    'X-Lampa-Expected-User': 'Alice', 'X-Lampa-Csrf': '1', 'If-Match': '"latest"'
  });
  assert.deepEqual(putBody(put), {schema_version: 2,
    data: {language: 'de', tmdb_lang: 'fr', favorite: '{"new":2}', torrserver_password: 'secret'},
    encrypt: realScope.encrypt});
  put.success('', 204, '"saved"');
  assert.equal(p.core.dirty('language'), false);
  assert.equal(JSON.parse(p.items[BASE]).tmdb_lang, p.core.fp('fr'));
  assert.equal(p.core.bootState().lastEtag, '"saved"');
  assert.deepEqual(clean(p.core.bootState().lastDoc), putBody(put).data);
  assert.equal(p.requests.length, p.initialRequests + 2); assert.deepEqual(p.notices, []);
});

test('empty and removed values save tombstones, clean the base, and stay clean after reboot', () => {
  for (const value of ['', null]) for (const missing of [false, true]) {
    const p = savingPage({torrserver_password: 'secret', language: 'en'});
    const get = startSave(p, 'torrserver_password', value);
    if (missing) get.fail(404, missingDocument); else get.success(undefined, 304);
    const put = p.requests.at(-1);
    assert.deepEqual(putBody(put).data, {torrserver_password: '', language: 'en'});
    assert.deepEqual(putBody(put).encrypt, realScope.encrypt);
    put.success('', 204, '"cleared"');
    assert.equal(p.core.dirty('torrserver_password'), false);
    assert.equal(Object.hasOwn(JSON.parse(p.items[BASE]), 'torrserver_password'), false);
    assert.deepEqual(clean(p.core.bootState().lastDoc), {torrserver_password: '', language: 'en'});
    p.tick(10000); assert.equal(p.requests.length, p.initialRequests + 2);
    const restarted = bootPage(p.items);
    restarted.scopeRequest.success(clean(realScope));
    restarted.dataRequest.success(documentV2(putBody(put).data), 200, '"cleared"');
    assert.equal(restarted.items.torrserver_password, '', 'server tombstone masks any reserve after reboot');
    assert.equal(restarted.core.dirty('torrserver_password'), false);
    assert.equal(restarted.core.disabled(), false, 'a successful clear never persists an invalid null fingerprint');
  }
});

test('edits during a clear PUT and clears during an edited PUT both remain dirty and save next', () => {
  for (const clear of ['', null]) for (const clearingFirst of [false, true]) {
    const p = savingPage({torrserver_password: 'secret'});
    const firstValue = clearingFirst ? clear : 'new secret';
    startSave(p, 'torrserver_password', firstValue).success(undefined, 304);
    const first = p.requests.at(-1), sent = firstValue || '';
    const secondValue = clearingFirst ? 'replacement secret' : clear;
    p.change('torrserver_password', secondValue);
    first.success('', 204, '"first"');
    assert.equal(p.core.dirty('torrserver_password'), true);
    assert.equal(p.items.torrserver_password, secondValue === null ? undefined : secondValue);
    const secondGet = p.requests.at(-1);
    assert.equal(secondGet.config.type, 'GET');
    secondGet.success(documentV2({torrserver_password: sent}), 200, '"first"');
    const second = p.requests.at(-1);
    assert.equal(putBody(second).data.torrserver_password, secondValue || '');
    second.success('', 204, '"second"');
    assert.equal(p.core.dirty('torrserver_password'), false);
    p.tick(10000); assert.equal(p.requests.length, p.initialRequests + 4);
  }
});

test('boot omission recovers a previously saved encrypted value through enableUploads without a write event', () => {
  const saved = savingPage({torrserver_password: 'old secret', file_view: 'progress'});
  startSave(saved, 'torrserver_password', 'saved secret').success(undefined, 304);
  saved.requests.at(-1).success('', 204, '"saved"');
  assert.equal(saved.core.dirty('torrserver_password'), false);
  const p = bootPage(saved.items);
  p.scopeRequest.success(clean(realScope));
  p.dataRequest.success(documentV2({}), 200, '"saved"');
  assert.equal(p.starts(), 1); assert.equal(p.items.torrserver_password, 'saved secret');
  assert.equal(p.core.dirty('torrserver_password'), true);
  assert.equal(p.core.dirty('file_view'), false);
  p.window.svtlv_account_state = signedIn('Alice'); p.install(); p.tick(100);
  p.tick(1999); assert.equal(p.requests.length, 2);
  p.tick(1);
  const get = p.requests.at(-1);
  assert.equal(get.config.headers['If-None-Match'], '"saved"');
  get.success(documentV2({}), 200, '"saved"');
  const put = p.requests.at(-1);
  assert.deepEqual(putBody(put).data, {torrserver_password: 'saved secret'});
  assert.deepEqual(putBody(put).encrypt, realScope.encrypt);
  put.success('', 204, '"recovered"');
  assert.equal(p.core.dirty('torrserver_password'), false);
  p.visibility(true);
  const count = p.requests.length;
  p.tick(600000); assert.equal(p.requests.length, count, 'successful recovery does not reschedule itself');
  const restarted = bootPage(p.items);
  restarted.scopeRequest.success(clean(realScope));
  restarted.dataRequest.success(documentV2(putBody(put).data), 200, '"recovered"');
  assert.equal(restarted.core.dirty('torrserver_password'), false);
});

test('live omission schedules one encrypted recovery, retaining plaintext and best-effort optional values', () => {
  const p = savingPage({torrserver_password: 'old secret', file_view: 'progress'});
  startSave(p, 'torrserver_password', 'saved secret').success(undefined, 304);
  p.requests.at(-1).success('', 204, '"saved"');
  p.visibility(false);
  p.requests.at(-1).success(documentV2({}), 200, '"saved"');
  assert.equal(p.items.torrserver_password, 'saved secret');
  assert.equal(p.core.dirty('torrserver_password'), true);
  assert.equal(p.core.dirty('file_view'), false);
  assert.equal(JSON.parse(p.items[BASE]).file_view, p.core.fp('progress'));
  assert.deepEqual(p.storageWrites, [], 'recovery never clears or rewrites the local credential');
  const before = p.requests.length;
  p.tick(1999); assert.equal(p.requests.length, before);
  p.tick(1);
  const get = p.requests.at(-1);
  assert.equal(get.config.headers['If-None-Match'], '"saved"');
  get.success(documentV2({}), 200, '"saved"');
  const put = p.requests.at(-1);
  assert.deepEqual(putBody(put).data, {torrserver_password: 'saved secret'});
  assert.deepEqual(putBody(put).encrypt, realScope.encrypt);
  put.success('', 204, '"recovered"');
  assert.equal(p.core.dirty('torrserver_password'), false);
  p.visibility(true);
  const count = p.requests.length;
  p.tick(600000); assert.equal(p.requests.length, count, 'omitted nonencrypted progress never forces a save loop');
});

test('a live remote encrypted tombstone never schedules recovery or restores the old credential', () => {
  const p = savingPage({torrserver_password: 'secret'});
  p.visibility(false);
  p.requests.at(-1).success(documentV2({torrserver_password: ''}), 200, '"cleared"');
  assert.equal(p.items.torrserver_password, '');
  assert.equal(p.core.dirty('torrserver_password'), false);
  const count = p.requests.length;
  p.tick(2000); assert.equal(p.requests.length, count);
  p.tick(58000); p.requests.at(-1).success(documentV2({}), 200, '"omitted"');
  assert.equal(p.core.dirty('torrserver_password'), false);
  p.visibility(true);
  p.tick(600000); assert.equal(p.requests.length, count + 1);
  assert.equal(p.requests.filter(request => request.config.type === 'PUT').length, 0);
});

test('change events only schedule: device keys, applying events, and clean writes do not save', () => {
  const p = savingPage();
  p.change('plugins', 'code'); p.change('language', 'en');
  p.core.whileApplying(() => p.change('language', 'ru'));
  p.tick(300000); assert.equal(p.requests.length, p.initialRequests);
  p.change('favorite', p.items.favorite); p.tick(2000);
  assert.equal(p.requests.length, p.initialRequests + 1, 'dirty is derived from storage, not the event payload');
});

test('an edit during an in-flight PUT stays dirty and multiple triggers queue just one more save', () => {
  const p = savingPage({language: 'en'});
  const get = startSave(p); get.success(documentV2({language: 'en'}), 200, '"before"');
  const first = p.requests.at(-1);
  assert.equal(putBody(first).data.language, 'ru');
  p.change('language', 'de'); p.change('language', 'fr'); p.visibility(true); p.visibility(true);
  p.tick(20000); assert.equal(p.requests.length, p.initialRequests + 2);
  first.success('', 204, '"first"');
  assert.equal(p.core.dirty('language'), true);
  assert.equal(JSON.parse(p.items[BASE]).language, p.core.fp('ru'));
  assert.equal(p.items.language, 'fr');
  const secondGet = p.requests.at(-1);
  assert.equal(p.requests.length, p.initialRequests + 3);
  assert.equal(secondGet.config.headers['If-None-Match'], '"first"');
  secondGet.success(documentV2({language: 'ru'}), 200, '"first"');
  assert.equal(p.items.language, 'fr', 'the save poll must not revert the new edit');
  const second = p.requests.at(-1);
  assert.equal(putBody(second).data.language, 'fr');
  second.success('', 204, '"second"');
  assert.equal(p.core.dirty('language'), false);
  p.tick(300000); assert.equal(p.requests.length, p.initialRequests + 4);
});

test('edits made during the pre-save GET are included in its PUT without parallel requests', () => {
  const p = savingPage({language: 'en'});
  const get = startSave(p);
  p.change('language', 'de'); p.change('torrserver_url', 'http://ts');
  p.tick(10000); assert.equal(p.requests.length, p.initialRequests + 1);
  get.success(undefined, 304);
  const put = p.requests.at(-1);
  assert.deepEqual(putBody(put).data, {language: 'de', torrserver_url: 'http://ts'});
  put.success('', 204);
  p.tick(10000); assert.equal(p.requests.length, p.initialRequests + 2, 'queued work already included needs no extra PUT');
});

test('optional changes wait five minutes without playback writes postponing the save', () => {
  const p = savingPage({file_view: '{}'});
  p.change('file_view', '{"one":1}'); p.tick(120000);
  p.change('file_view', '{"two":2}'); p.tick(120000);
  p.change('file_view', '{"three":3}'); p.tick(59999);
  assert.equal(p.requests.length, p.initialRequests);
  p.tick(1); p.requests.at(-1).success(undefined, 304);
  assert.deepEqual(putBody(p.requests.at(-1)).data, {file_view: '{"three":3}'});
  p.requests.at(-1).success('', 204);
  p.tick(300000); assert.equal(p.requests.length, p.initialRequests + 2);
});

test('must-sync dirtiness promotes an optional timer to two seconds', () => {
  const p = savingPage({file_view: '{}', language: 'en'});
  p.change('file_view', '{"x":1}'); p.tick(10000);
  p.change('language', 'ru'); p.tick(1999); assert.equal(p.requests.length, p.initialRequests);
  p.tick(1); assert.equal(p.requests.length, p.initialRequests + 1);
});

test('hiding flushes optional edits immediately and further changes can save while hidden', () => {
  const p = savingPage({file_view: '{}'});
  p.change('file_view', '{"x":1}'); p.tick(1000); p.visibility(true);
  assert.equal(p.requests.length, p.initialRequests + 1);
  p.requests.at(-1).success(undefined, 304); p.requests.at(-1).success('', 204);
  p.change('file_view', '{"x":2}'); p.tick(300000);
  p.requests.at(-1).success(undefined, 304);
  assert.deepEqual(putBody(p.requests.at(-1)).data, {file_view: '{"x":2}'});
  p.requests.at(-1).success('', 204); p.tick(300000);
  assert.equal(p.requests.length, p.initialRequests + 4);
});

test('412 polls the new document and retries once, retaining the other device changes', () => {
  const p = savingPage({language: 'en'});
  startSave(p).success(documentV2({language: 'en'}), 200, '"v1"');
  p.requests.at(-1).fail(412);
  const retryGet = p.requests.at(-1);
  assert.equal(retryGet.config.type, 'GET');
  assert.equal(retryGet.config.headers['If-None-Match'], '"v1"');
  retryGet.success(documentV2({language: 'en', favorite: '{"remote":1}'}), 200, '"v2"');
  const retry = p.requests.at(-1);
  assert.equal(retry.config.headers['If-Match'], '"v2"');
  assert.deepEqual(putBody(retry).data, {language: 'ru', favorite: '{"remote":1}'});
  retry.success('', 204, '"v3"');
  assert.equal(p.core.dirty('language'), false); assert.deepEqual(p.notices, []);
  assert.equal(p.requests.length, p.initialRequests + 4);
});

test('a second 412 stops retrying, leaves the edit dirty and waits for the next trigger', () => {
  const p = savingPage({language: 'en'});
  startSave(p).success(undefined, 304); p.requests.at(-1).fail(412);
  p.requests.at(-1).success(documentV2({language: 'de'}), 200, '"v2"');
  p.requests.at(-1).fail(412); p.tick(600000);
  assert.equal(p.requests.length, p.initialRequests + 4); assert.equal(p.core.dirty('language'), true);
  assert.deepEqual(p.notices, ['Not saved to Account']);
  p.change('language', 'fr'); p.tick(2000);
  assert.equal(p.requests.length, p.initialRequests + 5);
});

test('404 before PUT exports all local synced values without If-Match', () => {
  const p = savingPage({language: 'en', favorite: '{"book":[1]}'});
  startSave(p).fail(404, missingDocument);
  const put = p.requests.at(-1);
  assert.equal(put.config.type, 'PUT'); assert.equal(put.config.headers['If-Match'], undefined);
  assert.deepEqual(putBody(put).data, {language: 'ru', favorite: '{"book":[1]}'});
  put.success('', 204); assert.equal(p.core.dirty('language'), false);
});

test('first login with no document schedules guest data for upload without another storage event', () => {
  const p = loginPage({items: {language: 'ru', favorite: '{"guest":1}'}});
  p.dataRequest.fail(404, missingDocument); const before = p.requests.length;
  p.tick(1999); assert.equal(p.requests.length, before);
  p.tick(1); p.requests.at(-1).fail(404, missingDocument);
  const put = p.requests.at(-1);
  assert.deepEqual(putBody(put).data, {language: 'ru', favorite: '{"guest":1}'});
  assert.equal(put.config.headers['If-Match'], undefined);
  put.success('', 204); assert.equal(p.core.dirty('favorite'), false);
});

test('normal upstream cached defaults are persisted and synced on first login', () => {
  for (const initialized of [false, true]) {
    const p = page({items: {language: 'en'}, upstreamStorage: true});
    const favorite = p.window.Lampa.Storage.get('favorite', '{}');
    if (initialized) {
      favorite.card = []; favorite.book = []; favorite.history = [];
    }
    const rawFavorite = JSON.stringify(favorite);
    assert.equal(p.items.favorite, undefined, 'upstream caches defaults without persisting them');
    p.state(signedIn('Alice')); p.requests[0].success(clean(realScope));
    p.requests.at(-1).fail(404, missingDocument);
    assert.equal(p.core.disabled(), false);
    assert.equal(p.core.accountState().uploads, true);
    assert.equal(p.items.favorite, rawFavorite);
    p.tick(2000); p.requests.at(-1).fail(404, missingDocument);
    const put = p.requests.at(-1);
    assert.deepEqual(putBody(put).data, {language: 'en', favorite: rawFavorite});
    put.success('', 204);
    assert.equal(p.core.dirty('favorite'), false); assert.deepEqual(p.notices, []);
  }
});

test('invalid GET 200 bodies, unexpected 404, and unexpected PUT success do not mark edits clean', () => {
  for (const body of [null, {}, {schema_version: 1, data: {}}, documentV2([]), documentV2(null)]) {
    const p = savingPage({language: 'en'});
    startSave(p).success(body);
    assert.equal(p.requests.length, p.initialRequests + 1); assert.equal(p.core.dirty('language'), true);
    assert.deepEqual(p.notices, ['Not saved to Account']);
  }
  const p = savingPage({language: 'en'});
  startSave(p).fail(404, {error: {code: 'other'}});
  assert.equal(p.requests.length, p.initialRequests + 1);
  p.change('language', 'de'); p.tick(2000); p.requests.at(-1).success(undefined, 304);
  p.requests.at(-1).success(documentV2({language: 'de'}), 200);
  assert.equal(p.core.dirty('language'), true); assert.equal(p.notices.length, 1);
});

for (const stage of ['GET', 'PUT']) {
  for (const status of [401, 409]) test(stage + ' ' + status + ' stops saves and sends expired once', () => {
    const p = savingPage({language: 'en'});
    const get = startSave(p);
    if (stage === 'PUT') get.success(undefined, 304);
    p.requests.at(-1).fail(status); const before = p.requests.length;
    assert.equal(p.core.accountState().uploads, false); assert.equal(expireCount(p), 1);
    assert.equal(p.core.dirty('language'), true); assert.equal(p.items[USER], 'Alice', 'account confirmation owns clearing');
    p.change('language', 'de'); p.visibility(true); p.tick(600000);
    assert.equal(p.requests.length, before); assert.deepEqual(p.notices, []);
    p.state(signedIn('Alice'));
    assert.equal(p.requests.length, before + 1, 'a confirmed session can recover');
  });
}

test('network and 5xx errors notify once per failure streak and retry on a later trigger', () => {
  for (const stage of ['GET', 'PUT']) for (const status of [0, 500, 503]) {
    const p = savingPage({language: 'en'});
    const get = startSave(p);
    if (stage === 'PUT') get.success(undefined, 304);
    p.requests.at(-1).fail(status);
    const before = p.requests.length;
    p.tick(600000); assert.equal(p.requests.length, before);
    assert.deepEqual(p.notices, ['Not saved to Account']); assert.equal(p.core.dirty('language'), true);
    p.change('language', 'de'); p.tick(2000); p.requests.at(-1).fail(status);
    assert.equal(p.notices.length, 1);
    p.visibility(true); p.requests.at(-1).success(undefined, 304); p.requests.at(-1).success('', 204);
    assert.equal(p.core.dirty('language'), false);
    p.change('language', 'fr'); p.tick(2000); p.requests.at(-1).fail(status);
    assert.equal(p.notices.length, 2, 'successful PUT ends the failure streak');
  }
});

test('client errors notify and leave local edits without a retry loop, including queued triggers', () => {
  for (const stage of ['GET', 'PUT']) for (const status of [400, 403, 413, 415]) {
    const p = savingPage({language: 'en'}); const get = startSave(p);
    if (stage === 'PUT') get.success(undefined, 304);
    p.change('language', 'de');
    p.requests.at(-1).fail(status); const before = p.requests.length;
    p.tick(600000); assert.equal(p.requests.length, before);
    assert.deepEqual(p.notices, ['Not saved to Account']); assert.equal(p.core.dirty('language'), true);
  }
});

test('failed saves survive a fresh page and the nolisten edit is uploaded automatically', () => {
  const p = savingPage({language: 'en'});
  startSave(p).success(undefined, 304); p.requests.at(-1).fail(503);
  const restarted = bootPage({...p.items});
  restarted.scopeRequest.success(clean(realScope));
  restarted.dataRequest.success(documentV2({language: 'en'}), 200, '"old"');
  restarted.window.svtlv_account_state = signedIn('Alice'); restarted.install(); restarted.tick(100);
  assert.equal(restarted.items.language, 'ru'); assert.equal(restarted.core.dirty('language'), true);
  restarted.tick(2000); restarted.requests.at(-1).success(undefined, 304);
  const put = restarted.requests.at(-1);
  assert.equal(putBody(put).data.language, 'ru'); put.success('', 204);
  assert.equal(restarted.core.dirty('language'), false);
});

test('logout, identity switch, cross-tab reload and storage failure cancel timers and ignore late PUTs', () => {
  for (const action of ['logout', 'switch', 'tab', 'storage']) {
    const p = savingPage({language: 'en'});
    startSave(p).success(undefined, 304); const put = p.requests.at(-1);
    p.change('language', 'de');
    if (action === 'logout') p.state(guestState);
    if (action === 'switch') p.state(signedIn('Bob'));
    if (action === 'tab') p.storageEvent({key: USER, oldValue: 'Alice', newValue: 'Bob'});
    if (action === 'storage') { p.faults.getItem = true; p.change('language', 'fr'); }
    const before = {...p.items}, requests = p.requests.length;
    put.success('', 204, '"late"'); put.fail(401); p.tick(600000);
    assert.deepEqual({...p.items}, before); assert.equal(p.requests.length, requests); assert.equal(expireCount(p), 0);
  }
  const p = savingPage({language: 'en'});
  p.change('language', 'ru'); p.state(guestState); const before = p.requests.length;
  p.tick(600000); assert.equal(p.requests.length, before);
});

test('size failures block PUTs and storage failures after PUT disable further uploads', () => {
  for (const value of ['x'.repeat(LIMIT), '\ud800']) {
    const p = savingPage({language: 'en'});
    startSave(p, 'language', value).success(undefined, 304);
    assert.equal(p.requests.length, p.initialRequests + 1); assert.equal(p.core.dirty('language'), true);
    assert.deepEqual(p.notices, ['Not saved to Account']);
  }
  const p = savingPage({language: 'en'});
  startSave(p).success(undefined, 304); p.faults.quota = BASE;
  p.requests.at(-1).success('', 204);
  assert.equal(p.core.disabled(), true); assert.equal(p.core.accountState().uploads, false);
  const before = p.requests.length; p.change('language', 'de'); p.tick(600000);
  assert.equal(p.requests.length, before); assert.deepEqual(p.notices, ['Account sync is unavailable on this device']);
});

function pollingPage(data = {}, options = {}) {
  return savingPage(data, {hidden: false, ...options});
}

test('visible pages poll every 60s with ETag; 304 leaves storage, base and caches untouched', () => {
  const p = pollingPage({language: 'en', favorite: '{"book":[1]}'});
  const before = {...p.items};
  const mutations = () => p.calls.filter(call => call.method === 'setItem' || call.method === 'removeItem');
  const writes = mutations();
  p.tick(59999); assert.equal(p.requests.length, p.initialRequests);
  p.tick(1);
  const get = p.requests.at(-1);
  assert.equal(get.config.type, 'GET');
  assert.deepEqual(clean(get.config.headers), {'X-Lampa-Expected-User': 'Alice', 'If-None-Match': '"initial"'});
  get.success(undefined, 304);
  assert.deepEqual({...p.items}, before); assert.deepEqual(mutations(), writes);
  assert.deepEqual(p.storageWrites, []); assert.deepEqual(p.refreshes, []);
  p.tick(60000); assert.equal(p.requests.length, p.initialRequests + 2);
  p.requests.at(-1).success(undefined, 304);
  assert.deepEqual(p.notices, []); assert.equal(p.reloads.length, 0);
});

test('live apply updates only differing non-dirty strings, must-sync first, without scheduling saves', () => {
  const p = pollingPage({language: 'en', favorite: '{"book":[1]}', file_view: '{}', tmdb_lang: 'en'});
  // A nolisten edit must be preserved without any event or save timer.
  p.items.language = 'ru';
  p.tick(60000);
  p.requests.at(-1).success(documentV2({file_view: '{ "progress": 0.5 }', language: 'de',
    favorite: '{ "book": [2] }', tmdb_lang: 'en', torrserver_url: 'http://remote',
    plugins: '["remote-code"]', file_view_123: 'cub', online_view: {invalid: true}, mine_reactions: true}), 200, '"remote"');
  assert.equal(p.items.language, 'ru'); assert.equal(p.core.dirty('language'), true);
  assert.equal(p.items.favorite, '{ "book": [2] }');
  assert.equal(p.items.file_view, '{ "progress": 0.5 }');
  assert.equal(p.items.torrserver_url, 'http://remote');
  assert.equal(p.items.plugins, undefined); assert.equal(p.items.file_view_123, undefined);
  assert.equal(p.items.online_view, ''); assert.equal(p.items.mine_reactions, '');
  const keys = p.storageWrites.map(w => w.key);
  assert.deepEqual(keys, ['favorite', 'torrserver_url', 'file_view']);
  assert.deepEqual(clean(p.storageWrites[0].value), {book: [2]});
  assert.deepEqual(p.refreshes, ['favorite', 'timeline']);
  assert.equal(p.core.dirty('favorite'), false); assert.equal(p.core.dirty('file_view'), false);
  assert.equal(JSON.parse(p.items[BASE]).favorite, p.core.fp(p.items.favorite));
  assert.equal(p.core.bootState().lastEtag, '"remote"');
  assert.equal(p.reloads.length, 0);
  p.tick(59999); assert.equal(p.requests.length, p.initialRequests + 1, 'apply change events must not schedule a PUT');
});

test('bookmark/progress memory refreshes only for successful writes of their active key', () => {
  const p = pollingPage({favorite: '{}', file_view: '{}'}, {timelineKey: 'online_view'});
  p.tick(60000);
  p.requests.at(-1).success(documentV2({favorite: '{}', file_view: '{"x":1}'}));
  assert.deepEqual(p.refreshes, []);
  p.visibility(false);
  p.requests.at(-1).success(documentV2({favorite: '{"x":1}', online_view: '{"time":2}'}));
  assert.deepEqual(p.refreshes, ['favorite', 'timeline']);
  p.visibility(false);
  p.requests.at(-1).success(documentV2({favorite: '{"x":1}', online_view: '{"time":2}'}));
  assert.deepEqual(p.refreshes, ['favorite', 'timeline']);
  p.items.favorite = '{"local":1}';
  p.visibility(false); p.requests.at(-1).success(documentV2({favorite: '{"remote":2}'}));
  assert.deepEqual(p.refreshes, ['favorite', 'timeline'], 'dirty favorite is not refreshed');
});

test('hidden pages stop periodic polls; screen return polls once and restarts the timer', () => {
  const p = pollingPage({language: 'en'}, {hidden: true});
  p.tick(180000); assert.equal(p.requests.length, p.initialRequests);
  p.visibility(false); assert.equal(p.requests.length, p.initialRequests + 1);
  // Repeated visible events join the existing request.
  p.visibility(false); assert.equal(p.requests.length, p.initialRequests + 1);
  p.requests.at(-1).success(undefined, 304);
  p.tick(59999); assert.equal(p.requests.length, p.initialRequests + 1);
  p.tick(1); assert.equal(p.requests.length, p.initialRequests + 2);
  p.requests.at(-1).success(undefined, 304);
  p.visibility(true); p.tick(180000);
  assert.equal(p.requests.length, p.initialRequests + 2);
  p.visibility(false); assert.equal(p.requests.length, p.initialRequests + 3);
});

test('saves join a running poll and polling never overlaps a PUT', () => {
  const p = pollingPage({language: 'en', favorite: '{}'});
  p.tick(60000); const get = p.requests.at(-1);
  p.change('language', 'ru'); p.tick(2000); p.visibility(false);
  assert.equal(p.requests.length, p.initialRequests + 1);
  get.success(documentV2({language: 'en', favorite: '{"remote":1}'}), 200, '"v2"');
  assert.equal(p.items.favorite, '{"remote":1}', 'pre-save poll also applies remote data live');
  const put = p.requests.at(-1);
  assert.equal(put.config.type, 'PUT'); assert.equal(put.config.headers['If-Match'], '"v2"');
  assert.deepEqual(putBody(put).data, {language: 'ru', favorite: '{"remote":1}'});
  p.visibility(false); p.tick(180000);
  assert.equal(p.requests.length, p.initialRequests + 2, 'timer skips while the PUT is active');
  put.success('', 204, '"written"');
  p.visibility(false);
  assert.equal(p.requests.at(-1).config.headers['If-None-Match'], '"written"');
  p.requests.at(-1).success(undefined, 304);
  assert.equal(p.core.dirty('language'), false);
  assert.deepEqual(p.refreshes, ['favorite']);
});

test('an idle poll remains single-flight across several timer ticks and visibility events', () => {
  const p = pollingPage(); p.tick(60000);
  const get = p.requests.at(-1);
  p.tick(180000); p.visibility(false); p.visibility(false);
  assert.equal(p.requests.length, p.initialRequests + 1);
  get.success(documentV2({language: 'ru'}), 200, '"remote"');
  p.tick(60000); assert.equal(p.requests.length, p.initialRequests + 2);
  assert.equal(p.requests.at(-1).config.headers['If-None-Match'], '"remote"');
});

test('invalid poll responses do not change local values, base, document or ETag', () => {
  for (const invalid of [null, {}, {schema_version: 1, data: {}}, documentV2([]), documentV2(null)]) {
    const p = pollingPage({language: 'en'});
    const before = {...p.items}; const doc = clean(p.core.bootState());
    p.tick(60000); p.requests.at(-1).success(invalid, 200, '"invalid"');
    assert.deepEqual({...p.items}, before); assert.deepEqual(clean(p.core.bootState()), doc);
    assert.deepEqual(p.storageWrites, []); assert.deepEqual(p.refreshes, []);
    assert.deepEqual(p.notices, ['Not saved to Account']);
    p.tick(60000); assert.equal(p.requests.at(-1).config.headers['If-None-Match'], '"initial"');
    p.requests.at(-1).success(documentV2({language: 'de'}), 200, '"valid"');
    assert.equal(p.items.language, 'de');
  }
});

test('poll auth failures stop all timers and ignore late data until a confirmed account recheck', () => {
  for (const status of [401, 409]) {
    const p = pollingPage({language: 'en'});
    p.tick(60000); const get = p.requests.at(-1); get.fail(status);
    assert.equal(expireCount(p), 1); assert.equal(p.core.accountState().uploads, false);
    const before = p.requests.length;
    p.visibility(false); p.change('language', 'ru'); p.tick(600000);
    get.success(documentV2({favorite: '{}'}));
    assert.equal(p.requests.length, before); assert.equal(p.items.favorite, '');
    assert.deepEqual(p.refreshes, []); assert.deepEqual(p.notices, []);
    p.state(signedIn('Alice'));
    assert.equal(p.requests.length, before + 1);
  }
});

test('logout, identity changes, reload and storage failure cancel polling and discard obsolete responses', () => {
  for (const action of ['logout', 'switch', 'tab', 'storage']) {
    const p = pollingPage({language: 'en'}); p.tick(60000); const get = p.requests.at(-1);
    if (action === 'logout') p.state(guestState);
    if (action === 'switch') p.state(signedIn('Bob'));
    if (action === 'tab') p.storageEvent({key: USER, oldValue: 'Alice', newValue: 'Bob'});
    if (action === 'storage') { p.faults.getItem = true; p.change('language', 'ru'); }
    const before = {...p.items}, count = p.requests.length;
    get.success(documentV2({language: 'old-account'})); get.fail(401);
    p.tick(600000); p.visibility(false);
    assert.deepEqual({...p.items}, before); assert.equal(p.requests.length, count);
    assert.equal(expireCount(p), 0);
  }
});

test('live quota handling skips optional writes and disables sync on must-sync failures', () => {
  const p = pollingPage({language: 'en', file_view: '{}'}, {upstreamStorage: true});
  p.faults.quota = 'file_view'; p.tick(60000);
  p.requests.at(-1).success(documentV2({file_view: '{"x":1}', language: 'ru'}));
  assert.equal(p.items.language, 'ru'); assert.equal(p.core.disabled(), false);
  assert.equal(JSON.parse(p.items[BASE]).file_view, p.core.fp('{}'));
  assert.deepEqual(p.refreshes, [], 'failed optional write does not refresh its cache');
  const q = pollingPage({language: 'en'}, {upstreamStorage: true});
  q.faults.quota = 'language'; q.tick(60000);
  q.requests.at(-1).success(documentV2({language: 'ru', file_view: '{}'}));
  assert.equal(q.core.disabled(), true); assert.equal(q.core.accountState().uploads, false);
  assert.deepEqual(q.storageWrites.map(w => w.key), ['language']);
  const count = q.requests.length; q.tick(600000); q.visibility(false);
  assert.equal(q.requests.length, count);
  assert.deepEqual(q.notices, ['Синхронизация Аккаунта недоступна на этом устройстве']);
});

test('live values round-trip through the actual upstream Storage cache just like fresh reads', () => {
  const data = {language: 'true', tmdb_lang: 'false', torrserver_auth: '0', torrserver_login: '0042',
    torrserver_password: '-2', favorite: '{ "book": [1, true, null] }',
    mine_reactions: '[ 1, "2" ]', file_view: '{"time": 1.5}', online_view: '[malformed',
    person_subscribes_id: 'null', user_clarifys: '1.5', search_history: ''};
  const p = pollingPage({}, {upstreamStorage: true});
  // Populate old in-memory values to ensure Storage.set actually invalidates them.
  for (const key of Object.keys(data)) p.window.Lampa.Storage.get(key);
  p.tick(60000); p.requests.at(-1).success(documentV2(data));
  const fresh = page({items: data, upstreamStorage: true});
  for (const [key, raw] of Object.entries(data)) {
    assert.equal(p.items[key], raw, key + ' preserves the exact raw string');
    assert.equal(p.core.dirty(key), false, key + ' remains clean despite JSON formatting');
    const actual = p.window.Lampa.Storage.get(key), expected = fresh.window.Lampa.Storage.get(key);
    assert.equal(typeof actual, typeof expected, key);
    assert.deepEqual(clean(actual), clean(expected), key);
  }
  assert.equal(p.window.Lampa.Storage.get('language'), true);
  assert.equal(p.window.Lampa.Storage.get('tmdb_lang'), false);
  assert.equal(p.window.Lampa.Storage.get('torrserver_auth'), 0);
  assert.equal(p.window.Lampa.Storage.get('torrserver_login'), 42);
  assert.deepEqual(clean(p.window.Lampa.Storage.get('favorite')), {book: [1, true, null]});
  assert.equal(p.window.Lampa.Storage.get('online_view'), '[malformed');
  assert.equal(p.reloads.length, 0);
});

test('idle polling recovers from network failures and starts a new notification streak after success', () => {
  const p = pollingPage({language: 'en'});
  p.tick(60000); p.requests.at(-1).fail(503);
  p.tick(60000); p.requests.at(-1).fail(0);
  assert.deepEqual(p.notices, ['Not saved to Account']);
  assert.equal(p.items.language, 'en'); assert.equal(p.core.bootState().lastEtag, '"initial"');
  p.tick(60000); p.requests.at(-1).success(documentV2({language: 'de'}), 200, '"recovered"');
  assert.equal(p.items.language, 'de'); assert.equal(p.core.accountState().uploads, true);
  p.tick(60000); p.requests.at(-1).fail(503);
  assert.deepEqual(p.notices, ['Not saved to Account', 'Not saved to Account']);
  assert.equal(p.reloads.length, 0);
});

// A shared document lets two independent copies of the unmodified add-on race.
// Go tests cover the real API/storage; this fixture only supplies its HTTP contract.
function sharedDocument(initial = null) {
  let data = initial === null ? null : clean(initial);
  const answered = new WeakSet(), responses = [];
  const unreadable = new Set();
  // Match the backend: repairing unchanged plaintext must preserve its ETag.
  const etag = () => '"' + createHash('sha256').update(JSON.stringify(
    Object.fromEntries(Object.entries(data || {}).sort(([a], [b]) => a.localeCompare(b)))
  )).digest('hex') + '"';
  return {
    responses,
    data: () => clean(data),
    etag,
    omitEncrypted(...keys) { for (const key of keys) unreadable.add(key); },
    respond(request) {
      assert.ok(request && !answered.has(request), 'respond to each queued request only once');
      answered.add(request);
      const {config} = request;
      assert.equal(config.url, '/api/v1/user-data');
      assert.equal(config.headers['X-Lampa-Expected-User'], 'Alice');
      let status;
      if (config.type === 'GET') {
        status = data === null ? 404 : !unreadable.size && config.headers['If-None-Match'] === etag() ? 304 : 200;
        responses.push(status);
        if (status === 404) request.fail(status, missingDocument);
        else {
          const readable = clean(data);
          for (const key of unreadable) delete readable[key];
          const body = documentV2(readable);
          if (unreadable.size) body.unreadable_keys = [...unreadable];
          request.success(status === 200 ? body : undefined, status, etag());
        }
      } else {
        assert.equal(config.type, 'PUT');
        assert.equal(config.headers['X-Lampa-Csrf'], '1');
        const expected = config.headers['If-Match'];
        if (expected && (data === null || expected !== etag())) {
          responses.push(412);
          request.fail(412, {error: {code: 'precondition_failed'}});
        } else {
          const body = putBody(request);
          assert.equal(body.schema_version, 2);
          data = clean(body.data); unreadable.clear();
          responses.push(204);
          request.success('', 204, etag());
        }
      }
    }
  };
}

function acceptanceDevice(server, options = {}, scope = realScope) {
  const p = page({...options, preboot: true, uninspected: true, upstreamStorage: true,
    state: signedIn('Alice'), items: {[USER]: 'Alice', ...options.items}});
  let starts = 0;
  p.window.svtlv_sync_boot(() => { starts++; p.install(); });
  p.requests.find(r => r.config.url.startsWith('svtlv/storage-scope.json')).success(clean(scope));
  server.respond(p.requests.find(r => r.config.url === '/api/v1/user-data'));
  p.tick(100);
  assert.equal(starts, 1);
  assert.equal(p.reloads.length, 0);
  assert.equal(p.requests.length, 2, 'successful boot needs no post-boot document fetch');
  // Measure later polling/storage events independently of startup cache refresh.
  p.storageWrites.length = 0;
  p.refreshes.length = 0;
  return p;
}

const torrserverConnection = {
  torrserver_url: 'http://tailnet:8090', torrserver_url_two: 'http://192.168.1.2:8090',
  torrserver_auth: 'true', torrserver_login: 'shared user', torrserver_password: 'shared secret'
};

test('two links: first guest upload and other client imports retain independent selections across reload', () => {
  const server = sharedDocument();
  const a = page({state: guestState, uninspected: true, upstreamStorage: true,
    items: {...torrserverConnection, torrserver_use_link: 'two'}});
  a.state(signedIn('Alice'));
  a.requests[0].success(clean(realScope)); server.respond(a.requests.at(-1));
  a.tick(2000); server.respond(a.requests.at(-1));
  const put = a.requests.at(-1);
  assert.deepEqual(putBody(put).data, torrserverConnection);
  assert.deepEqual(putBody(put).encrypt, realScope.encrypt);
  server.respond(put);
  assert.deepEqual(server.data(), torrserverConnection);
  const b = acceptanceDevice(server, {items: {torrserver_use_link: 'one'}});
  const fresh = acceptanceDevice(server);
  for (const [p, selection] of [[a, 'two'], [b, 'one'], [fresh, 'one']]) {
    for (const [key, value] of Object.entries(torrserverConnection)) assert.equal(p.items[key], value, key);
    assert.equal(p.window.Lampa.Storage.field('torrserver_use_link'), selection);
    assert.equal(Object.hasOwn(JSON.parse(p.items[BASE]), 'torrserver_use_link'), false);
    const restarted = acceptanceDevice(server, {items: p.items, hidden: true});
    assert.equal(restarted.window.Lampa.Storage.field('torrserver_use_link'), selection);
    restarted.tick(600000);
    assert.equal(restarted.requests.length, 2, 'clean imported configuration does not upload on reload');
  }
});

for (const local of ['one', 'two', undefined]) {
  test('two links: remote selectors are ignored and local switches never save: ' + local, () => {
    const server = sharedDocument({...torrserverConnection, torrserver_use_link: local === 'two' ? 'one' : 'two'});
    const p = acceptanceDevice(server, {hidden: true,
      items: local ? {torrserver_use_link: local} : {}});
    assert.equal(p.window.Lampa.Storage.field('torrserver_use_link'), local || 'one');
    assert.equal(p.items.torrserver_use_link, local, 'import does not even persist a default selector');
    const before = {...p.items}, document = server.data();
    for (const selection of ['two', 'one', 'two']) {
      p.window.Lampa.Storage.set('torrserver_use_link', selection);
      p.tick(600000);
      assert.equal(p.requests.length, 2, 'a selector event does not dirty the export or start a save');
      assert.equal(p.items[BASE], before[BASE]);
      assert.equal(p.items.torrserver_url, before.torrserver_url);
      assert.equal(p.items.torrserver_url_two, before.torrserver_url_two);
    }
    assert.deepEqual(server.data(), document);
    p.visibility(false); const poll = p.requests.at(-1);
    poll.success(documentV2({...torrserverConnection, torrserver_use_link: 'one'}), 200, '"remote"');
    assert.equal(p.window.Lampa.Storage.field('torrserver_use_link'), 'two', 'live import also ignores selector');
    p.visibility(true); p.tick(600000);
    assert.equal(p.requests.filter(r => r.config.type === 'PUT').length, 0);
  });
}

for (const clear of ['', null]) {
  test('two links: URL edits poll live and secondary clears survive saves and reload: ' + clear, () => {
    const server = sharedDocument(torrserverConnection);
    const a = acceptanceDevice(server, {items: {torrserver_use_link: 'one'}});
    const b = acceptanceDevice(server, {items: {torrserver_use_link: 'two'}});
    const updated = {...torrserverConnection, torrserver_url: 'https://new-tailnet',
      torrserver_url_two: 'http://192.168.1.3:8090'};
    a.change('torrserver_url', updated.torrserver_url);
    a.change('torrserver_url_two', updated.torrserver_url_two);
    a.tick(2000); server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
    b.tick(60000); server.respond(b.requests.at(-1));
    for (const [key, value] of Object.entries(updated)) assert.equal(b.window.Lampa.Storage.get(key) + '', value, key);
    assert.equal(b.window.Lampa.Storage.field('torrserver_use_link'), 'two');
    b.change('torrserver_url_two', clear); b.tick(2000); server.respond(b.requests.at(-1));
    assert.equal(putBody(b.requests.at(-1)).data.torrserver_url_two, '');
    server.respond(b.requests.at(-1));
    a.tick(60000); server.respond(a.requests.at(-1));
    assert.equal(a.window.Lampa.Storage.get('torrserver_url_two'), '');
    a.change('torrserver_login', 'changed shared user'); a.tick(2000);
    server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
    assert.deepEqual(server.data(), {...updated, torrserver_url_two: '', torrserver_login: 'changed shared user'});
    for (const [p, selection] of [[a, 'one'], [b, 'two']]) {
      const restarted = acceptanceDevice(server, {items: p.items, hidden: true,
        reserve: {torrserver_url_two: 'http://old-reserve:8090'}});
      assert.equal(restarted.window.Lampa.Storage.get('torrserver_url_two'), '');
      assert.equal(restarted.window.Lampa.Storage.field('torrserver_use_link'), selection);
      assert.equal(Object.hasOwn(JSON.parse(restarted.items[BASE]), 'torrserver_url_two'), false);
      restarted.tick(600000);
      assert.equal(restarted.requests.length, 2);
    }
  });
}

for (const secondary of ['http://new-lan:8090', '']) for (const firstWriter of [0, 1]) {
  test('two links: stale-save retry merges independent URL edits without syncing selection: ' + secondary + ' / ' + firstWriter, () => {
    const server = sharedDocument(torrserverConnection);
    const devices = ['one', 'two'].map(selection => acceptanceDevice(server, {items: {torrserver_use_link: selection}}));
    devices[0].change('torrserver_url', 'https://new-tailnet');
    devices[1].change('torrserver_url_two', secondary);
    for (const p of devices) { p.tick(2000); server.respond(p.requests.at(-1)); }
    const winner = devices[firstWriter], retrying = devices[1 - firstWriter];
    assert.equal(winner.requests.at(-1).config.headers['If-Match'], retrying.requests.at(-1).config.headers['If-Match']);
    server.respond(winner.requests.at(-1)); server.respond(retrying.requests.at(-1));
    assert.equal(server.responses.at(-1), 412);
    const selection = retrying.items.torrserver_use_link;
    retrying.change('torrserver_use_link', selection === 'one' ? 'two' : 'one');
    server.respond(retrying.requests.at(-1));
    assert.equal(Object.hasOwn(putBody(retrying.requests.at(-1)).data, 'torrserver_use_link'), false);
    server.respond(retrying.requests.at(-1));
    winner.tick(60000); server.respond(winner.requests.at(-1));
    assert.deepEqual(server.data(), {...torrserverConnection, torrserver_url: 'https://new-tailnet', torrserver_url_two: secondary});
    for (const p of devices) {
      assert.equal(p.items.torrserver_url, 'https://new-tailnet');
      assert.equal(p.window.Lampa.Storage.get('torrserver_url_two', ''), secondary);
      assert.equal(p.reloads.length, 0); assert.deepEqual(p.notices, []);
    }
    assert.equal(retrying.items.torrserver_use_link, selection === 'one' ? 'two' : 'one');
    assert.equal(winner.items.torrserver_use_link, firstWriter === 0 ? 'one' : 'two');
  });
}

for (const previous of ['guest', 'Bob']) for (const secondary of [false, true]) {
  test('two links: existing account replaces guest or previous-owner URLs and logout retains selector: ' + previous + ' / ' + secondary, () => {
    const data = {...torrserverConnection};
    if (!secondary) delete data.torrserver_url_two;
    const server = sharedDocument(data);
    const p = loginPage({upstreamStorage: true, items: {
      ...(previous === 'Bob' ? {[USER]: 'Bob'} : {}), torrserver_use_link: 'two',
      torrserver_url: 'http://private-old-primary', torrserver_url_two: 'http://private-old-secondary',
      torrserver_password: 'private old password'
    }});
    server.respond(p.dataRequest);
    assert.equal(p.items[USER], 'Alice');
    assert.equal(p.items.torrserver_url, data.torrserver_url);
    assert.equal(p.items.torrserver_url_two, data.torrserver_url_two || '', 'absent secondary clears previous-owner/guest data');
    assert.equal(p.items.torrserver_password, data.torrserver_password);
    assert.equal(p.items.torrserver_use_link, 'two');
    const restarted = acceptanceDevice(server, {items: p.items});
    restarted.tick(2000);
    assert.equal(restarted.requests.length, 2, 'guest data must never be uploaded over an existing account document');
    restarted.state(guestState);
    for (const key of Object.keys(torrserverConnection)) assert.equal(restarted.items[key], '', key);
    assert.equal(restarted.items.torrserver_use_link, 'two');
    assert.equal(restarted.items[USER], undefined);
    assert.equal(restarted.items[BASE], undefined);
    assert.equal(restarted.requests.filter(r => r.config.type === 'PUT').length, 0);
  });
}

for (const key of ['torrserver_url', 'torrserver_url_two']) {
  test('two links: failed live URL persistence disables uploads and retains cleanup ownership: ' + key, () => {
    const server = sharedDocument(torrserverConnection);
    const p = acceptanceDevice(server, {items: {torrserver_use_link: 'two'}});
    p.faults.quota = key;
    p.tick(60000);
    p.requests.at(-1).success(documentV2({...torrserverConnection, [key]: 'http://new-server'}), 200, '"new"');
    assert.equal(p.items[key], undefined, 'upstream quota fallback removes the raw item');
    assert.equal(p.window.Lampa.Storage.get(key), 'http://new-server', 'the incoming value remains in memory');
    assert.equal(p.items[USER], 'Alice');
    const requests = p.requests.length;
    p.change('torrserver_url', 'http://another-edit'); p.tick(600000);
    assert.equal(p.requests.length, requests, 'failed must-sync persistence stops polling and saves');
    assert.equal(p.requests.filter(r => r.config.type === 'PUT').length, 0);
    assert.deepEqual(server.data(), torrserverConnection);
    delete p.faults.quota;
    p.state(guestState);
    for (const field of Object.keys(torrserverConnection)) assert.equal(p.items[field], '', field);
    assert.equal(p.items.torrserver_use_link, 'two');
    assert.equal(p.items[USER], undefined);
  });
}

for (const mode of ['login', 'recovery', 'omitted']) test('account application refreshes stale guest caches without a raw change: ' + mode, () => {
  const server = sharedDocument(mode === 'omitted' ? {} : {favorite: '{"book":[2]}'});
  const sharedItems = {favorite: '{"book":[1]}', ...(mode === 'recovery' ? {[USER]: 'Alice'} : {})};
  const p = page({sharedItems, upstreamStorage: true, uninspected: true});
  assert.deepEqual(clean(p.window.Lampa.Storage.get('favorite')), {book: [1]});
  sharedItems.favorite = mode === 'omitted' ? '' : '{"book":[2]}';
  p.storageEvent({key: 'favorite', oldValue: '{"book":[1]}', newValue: sharedItems.favorite});
  p.state(signedIn('Alice'));
  p.requests[0].success(clean(realScope)); server.respond(p.requests.at(-1));
  assert.equal(p.reloads.length, 0, 'equal raw values keep the no-reload login path');
  const favorite = p.window.Lampa.Storage.get('favorite', {book: []});
  assert.deepEqual(clean(favorite), {book: mode === 'omitted' ? [] : [2]});
  assert.ok(p.refreshes.includes('favorite'), 'refresh the module cache as well as Storage');
  favorite.book.push(3); p.window.Lampa.Storage.set('favorite', favorite);
  p.tick(2000); server.respond(p.requests.at(-1)); server.respond(p.requests.at(-1));
  assert.deepEqual(JSON.parse(server.data().favorite), {book: mode === 'omitted' ? [3] : [2, 3]});
});

test('clearing ownership retains a fresh revision when the baseline returns to absence', () => {
  const p = loginPage();
  p.core.clearSynced();
  const revision = p.items[REVISION];
  assert.equal(typeof revision, 'string');
  assert.equal(p.items[BASE], undefined); assert.equal(p.items[USER], undefined);
  p.core.clearSynced();
  assert.notEqual(p.items[REVISION], revision);
  const stale = p.dataRequest;
  stale.success(documentV2({favorite: 'obsolete private data'}));
  assert.equal(p.items.favorite, '', 'an intervening cleanup invalidates the earlier login response');
  assert.notEqual(p.requests.at(-1), stale);
  p.requests.at(-1).success(documentV2({}));
  assert.equal(p.core.accountState().uploads, true);
});

for (const mode of ['poll', 'boot', 'scope']) test('delayed GETs reject a baseline that returned to its original contents: ' + mode, () => {
  const original = '{"book":["original"]}';
  const server = sharedDocument({favorite: original});
  const sharedItems = {[USER]: 'Alice'};
  const b = acceptanceDevice(server, {sharedItems});
  const a = mode === 'poll' ? acceptanceDevice(server, {sharedItems}) :
    page({preboot: true, sharedItems, upstreamStorage: true, uninspected: true, state: signedIn('Alice')});
  const remote = acceptanceDevice(server);
  remote.change('favorite', '{"book":["original","removed"]}'); remote.tick(2000);
  server.respond(remote.requests.at(-1)); server.respond(remote.requests.at(-1));
  if (mode === 'poll') a.tick(60000);
  else a.window.svtlv_sync_boot(() => a.install());
  const stale = a.requests.at(-1), staleData = documentV2(server.data()), staleTag = server.etag();
  if (mode === 'boot') a.requests[0].success(clean(realScope));
  if (mode === 'scope') stale.success(staleData, 200, staleTag);
  const originalBase = sharedItems[BASE];
  for (const favorite of ['{"book":["original","temporary"]}', original]) {
    b.change('favorite', favorite); b.tick(2000);
    server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  }
  assert.equal(sharedItems[BASE], originalBase, 'the fingerprints really return to the snapshot');
  if (mode === 'scope') a.requests[0].success(clean(realScope));
  else stale.success(staleData, 200, staleTag);
  assert.equal(sharedItems.favorite, original, 'a completed deletion cannot be undone by the delayed GET');
  assert.notEqual(a.requests.at(-1), stale, 'a repeated baseline still requires a fresh GET');
  server.respond(a.requests.at(-1));
  if (mode !== 'poll') a.tick(100);
  const favorite = a.window.Lampa.Storage.get('favorite');
  favorite.book.push('added'); a.window.Lampa.Storage.set('favorite', favorite);
  a.tick(2000); server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
  assert.deepEqual(JSON.parse(server.data().favorite), {book: ['original', 'added']});
});

test('a delayed PUT acknowledgement rejects a baseline that returned to its original contents', () => {
  const original = '{"book":["original"]}';
  const server = sharedDocument({favorite: original});
  const sharedItems = {[USER]: 'Alice'};
  const a = acceptanceDevice(server, {sharedItems}), b = acceptanceDevice(server, {sharedItems});
  a.window.Lampa.Storage.set('favorite', {book: ['original', 'removed']}); a.tick(2000);
  server.respond(a.requests.at(-1));
  const put = a.requests.at(-1), success = put.config.success, sentBase = sharedItems[BASE];
  let acknowledge;
  put.config.success = (...args) => { acknowledge = () => success(...args); };
  server.respond(put);
  for (const favorite of ['{"book":["original","temporary"]}', original]) {
    b.window.Lampa.Storage.set('favorite', favorite); b.tick(2000);
    server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  }
  assert.equal(sharedItems[BASE], sentBase);
  acknowledge();
  assert.equal(sharedItems[BASE], sentBase, 'the old acknowledgement cannot restore obsolete fingerprints');
  assert.equal(a.requests.at(-1).config.type, 'GET');
  server.respond(a.requests.at(-1));
  const favorite = a.window.Lampa.Storage.get('favorite');
  favorite.book.push('added'); a.window.Lampa.Storage.set('favorite', favorite);
  a.tick(2000); server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
  assert.deepEqual(JSON.parse(server.data().favorite), {book: ['original', 'added']});
});

test('delayed polls and pre-save GETs cannot roll back a sibling tab save', () => {
  for (const mode of ['poll', 'save', 'joined']) {
    const server = sharedDocument({language: 'en', tmdb_lang: 'en'});
    const sharedItems = {[USER]: 'Alice'};
    const a = acceptanceDevice(server, {sharedItems});
    const b = acceptanceDevice(server, {sharedItems});
    const remote = acceptanceDevice(server);
    remote.change('language', 'de'); remote.tick(2000);
    server.respond(remote.requests.at(-1)); server.respond(remote.requests.at(-1));
    if (mode === 'save') { a.change('tmdb_lang', 'fr'); a.tick(2000); }
    else a.tick(60000);
    const stale = a.requests.at(-1), staleData = documentV2(server.data()), staleTag = server.etag();
    if (mode === 'joined') {
      a.change('tmdb_lang', 'fr'); a.tick(2000);
      assert.equal(a.requests.at(-1), stale, 'the save joins the already pending poll');
    }
    assert.notEqual(stale.config.headers['If-None-Match'], staleTag, 'the delayed GET really returns 200');
    b.change('language', 'ru'); b.tick(2000);
    server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
    const savedBase = sharedItems[BASE];
    stale.success(staleData, 200, staleTag);
    assert.equal(sharedItems.language, 'ru', 'an older response cannot replace the saved shared value');
    assert.equal(sharedItems[BASE], savedBase, 'an older response cannot rewind shared fingerprints');
    assert.equal(a.requests.at(-1).config.type, 'GET', 'refetch before applying or uploading');
    assert.notEqual(a.requests.at(-1), stale);
    server.respond(a.requests.at(-1));
    if (mode !== 'poll') server.respond(a.requests.at(-1));
    // Continue the original failure sequence: a sibling saves an unrelated key,
    // then this tab saves another edit. Neither can turn stale data into a local edit.
    b.change('tmdb_lang', 'es'); b.tick(2000);
    server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
    a.change('tmdb_lang', 'it'); a.tick(2000);
    server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
    assert.deepEqual(server.data(), {language: 'ru', tmdb_lang: 'it'});
    assert.deepEqual(a.notices, []); assert.deepEqual(b.notices, []);
    assert.equal(a.reloads.length, 0); assert.equal(b.reloads.length, 0);
  }
});

for (const queued of [false, true]) test('a delayed PUT acknowledgement preserves shared and remote edits, queued=' + queued, () => {
  const server = sharedDocument({language: 'en', tmdb_lang: 'en'});
  const sharedItems = {[USER]: 'Alice'};
  const a = acceptanceDevice(server, {sharedItems});
  const b = acceptanceDevice(server, {sharedItems});
  const remote = acceptanceDevice(server);
  a.change('language', 'ru'); a.tick(2000);
  server.respond(a.requests.at(-1));
  const put = a.requests.at(-1), success = put.config.success;
  let acknowledge;
  put.config.success = (...args) => { acknowledge = () => success(...args); };
  server.respond(put);
  assert.equal(server.data().language, 'ru', 'the first save commits before its response arrives');
  b.change('language', 'fr'); b.tick(2000);
  server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  const savedBase = sharedItems[BASE];
  if (queued) a.change('tmdb_lang', 'it');
  acknowledge();
  assert.equal(sharedItems[BASE], savedBase, 'a delayed acknowledgement cannot rewind shared fingerprints');
  assert.equal(a.requests.at(-1).config.type, 'GET', 'reconcile a superseded acknowledgement with the server');
  server.respond(a.requests.at(-1));
  if (queued) {
    assert.equal(a.requests.at(-1).config.type, 'PUT');
    server.respond(a.requests.at(-1));
    assert.deepEqual(server.data(), {language: 'fr', tmdb_lang: 'it'}, 'an edit during the delayed response still saves');
  }
  remote.change('language', 'de'); remote.tick(2000);
  server.respond(remote.requests.at(-1)); server.respond(remote.requests.at(-1));
  a.change('tmdb_lang', 'es'); a.tick(2000);
  server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
  assert.deepEqual(server.data(), {language: 'de', tmdb_lang: 'es'});
  assert.deepEqual(a.notices, []); assert.equal(a.reloads.length, 0);
});

test('a PUT acknowledgement from a previous shared owner cannot modify the new account baseline', () => {
  const p = savingPage({language: 'en'});
  startSave(p).success(undefined, 304);
  const put = p.requests.at(-1);
  p.items[USER] = 'Bob'; p.items.language = 'fr'; p.syncedBase({language: 'fr'});
  const before = {...p.items};
  put.success('', 204, '"old-account"');
  assert.deepEqual({...p.items}, before);
  assert.equal(p.reloads.length, 1, 'refresh identity even before the sibling storage event arrives');
});

test('boot refetches an old document when shared fingerprints advance before scope or data arrives', () => {
  for (const scopeFirst of [false, true]) {
    const server = sharedDocument({language: 'en'});
    const sharedItems = {[USER]: 'Alice'};
    const b = acceptanceDevice(server, {sharedItems});
    const a = page({preboot: true, sharedItems, state: signedIn('Alice')});
    let starts = 0;
    a.window.svtlv_sync_boot(() => { starts++; a.install(); });
    const scopeRequest = a.requests[0], stale = a.requests[1];
    const staleData = documentV2(server.data()), staleTag = server.etag();
    if (scopeFirst) scopeRequest.success(clean(realScope));
    else stale.success(staleData, 200, staleTag);
    b.change('language', 'ru'); b.tick(2000);
    server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
    const savedBase = sharedItems[BASE];
    if (scopeFirst) stale.success(staleData, 200, staleTag);
    else scopeRequest.success(clean(realScope));
    assert.equal(starts, 0, 'startup waits for a fresh document within the original deadline');
    assert.equal(sharedItems.language, 'ru'); assert.equal(sharedItems[BASE], savedBase);
    assert.equal(a.requests.length, 3);
    server.respond(a.requests.at(-1));
    assert.equal(starts, 1); a.tick(100);
    assert.equal(sharedItems.language, 'ru'); assert.equal(a.core.accountState().uploads, true);
    assert.equal(a.reloads.length, 0);
  }
});

test('account recovery refetches a document superseded by another tab save', () => {
  const server = sharedDocument({language: 'en'});
  const sharedItems = {[USER]: 'Alice'};
  const b = acceptanceDevice(server, {sharedItems});
  const a = page({sharedItems});
  a.state(signedIn('Alice')); a.requests[0].success(clean(realScope));
  const stale = a.requests.at(-1), staleData = documentV2(server.data()), staleTag = server.etag();
  b.change('language', 'ru'); b.tick(2000);
  server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  const savedBase = sharedItems[BASE];
  stale.success(staleData, 200, staleTag);
  assert.equal(sharedItems.language, 'ru'); assert.equal(sharedItems[BASE], savedBase);
  assert.equal(a.reloads.length, 0); assert.equal(a.core.accountState().uploads, false);
  assert.notEqual(a.requests.at(-1), stale);
  server.respond(a.requests.at(-1));
  assert.equal(a.core.accountState().uploads, true); assert.equal(a.reloads.length, 0);
});

test('boot refetch keeps the original deadline and ignores its late response', () => {
  const p = page({preboot: true, items: {[USER]: 'Alice', language: 'en'}});
  p.syncedBase({language: 'en'});
  let starts = 0;
  p.window.svtlv_sync_boot(() => starts++);
  p.requests[0].success(clean(realScope));
  p.tick(2500);
  p.items.language = 'ru'; p.syncedBase({language: 'ru'});
  const savedBase = p.items[BASE];
  p.requests[1].success(documentV2({language: 'en'}));
  assert.equal(p.requests.length, 3); assert.equal(starts, 0);
  p.tick(500);
  assert.equal(starts, 1, 'refetch cannot restart the three-second deadline');
  p.requests[2].success(documentV2({language: 'de'}));
  assert.equal(starts, 1); assert.equal(p.requests.length, 3);
  assert.equal(p.items.language, 'ru'); assert.equal(p.items[BASE], savedBase);
  assert.equal(p.core.bootState().ready, false);
});

test('shared tabs refresh saved fingerprints before merging a newer remote edit', () => {
  const server = sharedDocument({language: 'en', tmdb_lang: 'en'});
  const sharedItems = {[USER]: 'Alice'};
  const a = acceptanceDevice(server, {sharedItems});
  const b = acceptanceDevice(server, {sharedItems});
  a.change('language', 'ru'); a.tick(2000);
  server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
  const remote = acceptanceDevice(server);
  remote.change('language', 'de'); remote.tick(2000);
  server.respond(remote.requests.at(-1)); server.respond(remote.requests.at(-1));
  b.change('tmdb_lang', 'fr'); b.tick(2000);
  server.respond(b.requests.at(-1));
  assert.deepEqual(putBody(b.requests.at(-1)).data, {language: 'de', tmdb_lang: 'fr'});
  server.respond(b.requests.at(-1));
  assert.deepEqual(server.data(), {language: 'de', tmdb_lang: 'fr'});
});

test('shared tabs refresh their own upstream caches before editing already-persisted remote data', () => {
  const server = sharedDocument({favorite: '{"book":[1]}', file_view: '{"movie":0.1}'});
  const sharedItems = {[USER]: 'Alice'};
  const a = acceptanceDevice(server, {sharedItems});
  const b = acceptanceDevice(server, {sharedItems});
  for (const p of [a, b]) {
    assert.deepEqual(clean(p.window.Lampa.Storage.get('favorite')), {book: [1]});
    assert.deepEqual(clean(p.window.Lampa.Storage.get('file_view')), {movie: 0.1});
  }
  a.window.Lampa.Storage.set('favorite', {book: [1, 2]});
  a.window.Lampa.Storage.set('file_view', {movie: 0.5});
  a.tick(2000); server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
  assert.equal(sharedItems.favorite, server.data().favorite, 'raw storage is already up to date');
  // No storage event is needed: each tab's poll must refresh its independent caches.
  b.tick(60000); server.respond(b.requests.at(-1));
  assert.deepEqual(clean(b.window.Lampa.Storage.get('favorite')), {book: [1, 2]});
  assert.deepEqual(clean(b.window.Lampa.Storage.get('file_view')), {movie: 0.5});
  assert.deepEqual(b.refreshes, ['favorite', 'timeline']);
  const favorite = b.window.Lampa.Storage.get('favorite');
  favorite.book.push(3);
  b.window.Lampa.Storage.set('favorite', favorite);
  b.tick(2000); server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  assert.deepEqual(JSON.parse(server.data().favorite), {book: [1, 2, 3]}, 'a later edit retains the sibling bookmark');
  assert.equal(b.reloads.length, 0); assert.deepEqual(b.notices, []);
});

test('synced storage events refresh tab caches without changing raw values or saved fingerprints', () => {
  const server = sharedDocument({language: 'en', favorite: '{"book":[1]}', file_view: '{"movie":0.1}'});
  const sharedItems = {[USER]: 'Alice'};
  const a = acceptanceDevice(server, {sharedItems});
  const b = acceptanceDevice(server, {sharedItems});
  b.window.Lampa.Storage.get('favorite'); b.window.Lampa.Storage.get('file_view');
  const savedBase = sharedItems[BASE];
  // A sibling edit is still dirty and its storage events can contain superseded values.
  a.window.Lampa.Storage.set('favorite', '{ "book": [1, 2] }');
  a.window.Lampa.Storage.set('file_view', '{ "movie": 0.5 }');
  b.storageEvent({key: 'favorite', oldValue: '{"book":[1]}', newValue: 'superseded'}, false);
  b.storageEvent({key: 'file_view', oldValue: '{"movie":0.1}', newValue: '{ "movie": 0.5 }'});
  assert.deepEqual(clean(b.window.Lampa.Storage.get('favorite')), {book: [1, 2]});
  assert.deepEqual(clean(b.window.Lampa.Storage.get('file_view')), {movie: 0.5});
  assert.deepEqual(b.refreshes, ['favorite', 'timeline']);
  assert.equal(sharedItems.favorite, '{ "book": [1, 2] }');
  assert.equal(sharedItems.file_view, '{ "movie": 0.5 }');
  assert.deepEqual(b.storageWrites.map(write => write.value), ['{ "book": [1, 2] }', '{ "movie": 0.5 }'],
    'event refresh writes identical raw strings instead of generating normalization events');
  assert.equal(sharedItems[BASE], savedBase, 'cache refresh never acknowledges an unsaved edit');
  b.tick(2000); assert.equal(b.requests.length, 2, 'a sibling cache refresh does not start a save');
  b.window.Lampa.Storage.set('language', 'ru');
  b.tick(2000); server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  assert.equal(server.responses.at(-2), 304, 'this tab can save the shared edit before its sibling');
  const favorite = b.window.Lampa.Storage.get('favorite');
  favorite.book.push(3); b.window.Lampa.Storage.set('favorite', favorite);
  b.tick(2000); server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  assert.deepEqual(JSON.parse(server.data().favorite), {book: [1, 2, 3]});
});

test('a sibling raw removal stops sync without restoring stale cached values', () => {
  for (const key of ['favorite', 'torrserver_password', 'file_view']) {
    const raw = key === 'torrserver_password' ? 'secret' : '{"book":[1]}';
    const server = sharedDocument({language: 'en', [key]: raw});
    const sharedItems = {[USER]: 'Alice'};
    const a = acceptanceDevice(server, {sharedItems});
    const b = acceptanceDevice(server, {sharedItems});
    b.window.Lampa.Storage.get(key);
    const savedBase = sharedItems[BASE];
    a.window.localStorage.removeItem(key);
    b.storageEvent({key, oldValue: raw, newValue: null});
    assert.equal(sharedItems[key], undefined, 'do not replace an ambiguous removal with stale data or a tombstone');
    assert.equal(sharedItems[BASE], savedBase, 'a raw removal is not acknowledged as saved');
    b.window.Lampa.Storage.set('language', 'ru');
    b.visibility(true); b.visibility(false); b.tick(600000);
    assert.equal(sharedItems[key], undefined, 'an unrelated edit cannot recover this tab\'s stale cache');
    assert.equal(b.requests.length, 2, 'stop polls and uploads after the removal');
    assert.equal(b.storageWrites.some(write => write.key === key), false);
    assert.deepEqual(b.notices, ['Account sync is unavailable on this device']);
    assert.deepEqual(server.data(), {language: 'en', [key]: raw});
  }
});

test('a sibling quota removal preserves reserve data for recovery after sync stops', async () => {
  const server = sharedDocument({language: 'en', favorite: '{"book":[1]}'});
  const sharedItems = {[USER]: 'Alice'}, reserve = {};
  const a = acceptanceDevice(server, {sharedItems, reserve});
  const b = acceptanceDevice(server, {sharedItems});
  b.window.Lampa.Storage.get('favorite');
  a.faults.quota = 'favorite';
  a.window.Lampa.Storage.set('favorite', {book: [1, 2]});
  await Promise.resolve();
  assert.equal(reserve.favorite, '{"book":[1,2]}', 'upstream has recovered the failed write in IndexedDB');
  b.storageEvent({key: 'favorite', oldValue: '{"book":[1]}', newValue: null});
  b.window.Lampa.Storage.set('language', 'ru');
  for (const p of [a, b]) {
    p.visibility(true); p.tick(600000);
    assert.equal(p.requests.length, 2, 'neither tab can upload a deletion or stale cache');
  }
  assert.equal(sharedItems.favorite, undefined, 'leave raw absence so it cannot mask the reserve');
  const restarted = acceptanceDevice(server, {sharedItems, reserve, hidden: true});
  assert.equal(sharedItems.favorite, '{"book":[1,2]}');
  restarted.tick(2000); server.respond(restarted.requests.at(-1));
  assert.equal(putBody(restarted.requests.at(-1)).data.favorite, '{"book":[1,2]}');
  server.respond(restarted.requests.at(-1));
  assert.equal(server.data().favorite, '{"book":[1,2]}');
  assert.deepEqual(restarted.notices, []);
});

test('upstream quota fallback never uploads a must-sync deletion tombstone', () => {
  for (const key of ['favorite', 'mine_reactions']) {
    const p = savingPage(undefined, {hidden: true, upstreamStorage: true});
    p.faults.quota = key;
    p.window.Lampa.Storage.set(key, {book: [1, 2]});
    assert.equal(p.items[key], undefined);
    assert.deepEqual(clean(p.window.Lampa.Storage.get(key)), {book: [1, 2]});
    p.tick(2000);
    assert.equal(p.core.disabled(), true);
    assert.equal(p.core.accountState().uploads, false);
    assert.equal(p.requests.length, p.initialRequests, 'preserve the server document on persistence failure');
  }
});

test('a restarted device persists a recovered must-sync reserve value before uploading it', () => {
  const server = sharedDocument({language: 'en', favorite: '{"book":[1]}'});
  const saved = acceptanceDevice(server);
  delete saved.items.favorite;
  const restarted = acceptanceDevice(server, {items: saved.items, hidden: true,
    reserve: {favorite: '{"book":[1,2]}'}});
  assert.deepEqual(clean(restarted.window.Lampa.Storage.get('favorite')), {book: [1, 2]});
  assert.equal(restarted.items.favorite, '{"book":[1,2]}');
  restarted.tick(2000); server.respond(restarted.requests.at(-1));
  assert.equal(putBody(restarted.requests.at(-1)).data.favorite, '{"book":[1,2]}');
  server.respond(restarted.requests.at(-1));
  assert.deepEqual(server.data(), {language: 'en', favorite: '{"book":[1,2]}'});
  assert.deepEqual(restarted.notices, []);
});

test('first login recovers writable must-sync reserve values without omitting them', () => {
  for (const key of ['favorite', 'torrserver_password', 'torrserver_url', 'torrserver_url_two']) {
    const raw = key === 'favorite' ? '{"book":[1,2]}' : 'private credential';
    const p = loginPage({items: {language: 'en'}, reserve: {[key]: raw}, upstreamStorage: true});
    p.dataRequest.fail(404, missingDocument);
    assert.equal(p.items[key], raw); assert.equal(p.core.disabled(), false);
    p.tick(2000); p.requests.at(-1).fail(404, missingDocument);
    assert.deepEqual(putBody(p.requests.at(-1)).data, {language: 'en', [key]: raw});
    p.requests.at(-1).success('', 204);
    assert.equal(p.core.dirty(key), false); assert.deepEqual(p.notices, []);
  }
});

test('first login disables uploads if must-sync reserve values still cannot be persisted', () => {
  for (const key of ['favorite', 'torrserver_password', 'torrserver_url', 'torrserver_url_two']) for (const otherLocalData of [false, true]) {
    const reserveValue = key === 'favorite' ? '{"book":[1,2]}' : 'private credential';
    const p = loginPage({items: otherLocalData ? {language: 'en'} : {},
      reserve: {[key]: reserveValue}, upstreamStorage: true});
    p.faults.quota = key;
    p.dataRequest.fail(404, missingDocument);
    assert.equal(p.items[key], undefined);
    assert.equal(p.core.disabled(), true, 'a must-sync fallback cannot be silently omitted on first upload');
    assert.equal(p.core.accountState().uploads, false);
    assert.deepEqual(clean(p.window.Lampa.Storage.get(key)), key === 'favorite' ? {book: [1, 2]} : reserveValue);
    p.tick(600000); p.visibility(true);
    assert.equal(p.requests.filter(r => r.config.type === 'PUT').length, 0);
    assert.deepEqual(p.notices, [otherLocalData ? 'Account sync is unavailable on this device' :
      'Синхронизация Аккаунта недоступна на этом устройстве']);
    delete p.faults.quota;
    p.state(guestState);
    assert.equal(p.items[key], ''); assert.equal(p.items[USER], undefined);
    assert.equal(p.reloads.length, 1, 'first-login persistence failures still permit logout cleanup');
  }
});

test('disabled sync still clears account data on logout, expiry or account replacement', () => {
  for (const failure of ['reserve', 'quota', 'read', 'base']) for (const next of [guestState, signedIn('Bob')]) {
    for (const leaving of [false, true]) {
      const p = savingPage({language: 'en', favorite: '{"book":[1]}'}, {upstreamStorage: true});
      p.window.Lampa.Storage.get('favorite');
      if (failure === 'reserve') {
        p.faults.quota = 'favorite';
        p.window.Lampa.Storage.set('favorite', {book: [1, 2]});
      } else if (failure === 'quota') {
        startSave(p).success(undefined, 304); p.faults.quota = BASE;
        p.requests.at(-1).success('', 204);
      } else if (failure === 'read') {
        p.faults.getItem = true; p.change('language', 'ru');
      } else {
        p.items[BASE] = '{'; p.change('language', 'ru');
      }
      assert.equal(p.core.disabled(), true);
      delete p.faults.quota; delete p.faults.getItem;
      const requests = p.requests.length;
      // Confirmed expiry uses the same guest event as explicit logout.
      p.window.svtlv_account_leaving = leaving && next.status === 'guest';
      p.state(next);
      assert.equal(p.items.favorite, ''); assert.equal(p.items.language, '');
      assert.equal(p.items[USER], undefined); assert.equal(p.items[BASE], undefined);
      assert.equal(p.reloads.length, p.window.svtlv_account_leaving ? 0 : 1);
      const fresh = page({items: p.items, upstreamStorage: true, reserve: {favorite: {book: ['private']}}});
      assert.equal(fresh.window.Lampa.Storage.get('favorite', ''), '');
      p.tick(600000); assert.equal(p.requests.length, requests, 'cleanup never re-enables synchronization');
    }
  }
});

test('an incomplete device refreshes after same-ETag repair without deleting the repaired credential', () => {
  for (const repairDuringSave of [false, true]) {
    const server = sharedDocument({language: 'en', torrserver_password: 'secret'});
    const a = acceptanceDevice(server);
    const originalTag = server.etag();
    server.omitEncrypted('torrserver_password');
    const b = acceptanceDevice(server);
    b.change('language', 'ru'); b.tick(2000);
    if (repairDuringSave) server.respond(b.requests.at(-1));
    a.tick(60000); server.respond(a.requests.at(-1));
    a.tick(2000); server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
    assert.equal(server.etag(), originalTag, 'identical-plaintext repair keeps the canonical ETag');
    if (!repairDuringSave) server.respond(b.requests.at(-1));
    else {
      assert.equal(b.requests.at(-1).config.type, 'GET', 'incomplete overlay cannot issue a replacement PUT');
      b.tick(60000); server.respond(b.requests.at(-1));
      b.tick(2000); server.respond(b.requests.at(-1));
    }
    const put = b.requests.at(-1);
    assert.equal(put.config.type, 'PUT');
    assert.equal(putBody(put).data.torrserver_password, 'secret');
    server.respond(put);
    assert.deepEqual(server.data(), {language: 'ru', torrserver_password: 'secret'});
  }
});

test('acceptance: guest stays local through edits and visibility changes; first login handles both document states', () => {
  for (const existing of [false, true]) {
    const server = sharedDocument(existing ? {language: 'en'} : null);
    const p = page({state: guestState, uninspected: true, upstreamStorage: true,
      items: {language: 'ru', favorite: '{"book":["guest"]}', player: 'native'}});
    let starts = 0;
    p.window.svtlv_sync_boot(() => starts++);
    p.change('tmdb_lang', 'ru'); p.visibility(true); p.visibility(false); p.tick(600000);
    assert.equal(starts, 1); assert.equal(p.requests.length, 0);
    p.state(signedIn('Alice'));
    p.requests[0].success(clean(realScope)); server.respond(p.requests.at(-1));
    assert.equal(p.items[USER], 'Alice');
    assert.equal(p.items.player, 'native');
    if (existing) {
      assert.equal(p.reloads.length, 1);
      assert.equal(p.items.language, 'en'); assert.equal(p.items.favorite, '');
      const restarted = acceptanceDevice(server, {items: p.reloads[0]});
      restarted.tick(2000);
      assert.equal(restarted.requests.length, 2, 'discarded guest data is never uploaded');
      assert.deepEqual(server.data(), {language: 'en'});
    } else {
      assert.equal(p.reloads.length, 0);
      p.tick(2000); server.respond(p.requests.at(-1));
      const put = p.requests.at(-1);
      assert.equal(put.config.headers['If-Match'], undefined);
      server.respond(put);
      assert.deepEqual(server.data(), {language: 'ru', tmdb_lang: 'ru', favorite: '{"book":["guest"]}'});
    }
  }
});

test('acceptance: a nolisten language edit survives a fresh boot and reaches the shared document', () => {
  const server = sharedDocument({language: 'en', tmdb_lang: 'en'});
  const first = acceptanceDevice(server);
  first.window.Lampa.Storage.set('language', 'ru', true);
  first.window.Lampa.Storage.set('tmdb_lang', 'ru', true);
  assert.equal(first.requests.length, 2, 'nolisten itself does not schedule a save');
  const restarted = acceptanceDevice(server, {items: first.items});
  assert.equal(restarted.items.language, 'ru'); assert.equal(restarted.items.tmdb_lang, 'ru');
  restarted.tick(2000); server.respond(restarted.requests.at(-1)); server.respond(restarted.requests.at(-1));
  assert.deepEqual(server.data(), {language: 'ru', tmdb_lang: 'ru'});
  const other = acceptanceDevice(server);
  assert.equal(other.window.Lampa.Storage.get('language'), 'ru');
  assert.equal(other.window.Lampa.Storage.get('tmdb_lang'), 'ru');
});

test('acceptance: empty and removed values clear a second device and survive saves and reboot', () => {
  for (const value of ['', null]) {
    const server = sharedDocument({language: 'en', torrserver_password: 'secret'});
    const a = acceptanceDevice(server), b = acceptanceDevice(server, {reserve: {torrserver_password: 'reserve secret'}});
    a.change('torrserver_password', value); a.tick(2000);
    server.respond(a.requests.at(-1));
    assert.equal(putBody(a.requests.at(-1)).data.torrserver_password, '');
    server.respond(a.requests.at(-1));
    assert.equal(Object.hasOwn(JSON.parse(a.items[BASE]), 'torrserver_password'), false);
    b.tick(60000); server.respond(b.requests.at(-1));
    assert.equal(b.items.torrserver_password, '');
    assert.equal(b.window.Lampa.Storage.get('torrserver_password', ''), '', 'the tombstone masks the old reserve');
    assert.equal(Object.hasOwn(JSON.parse(b.items[BASE]), 'torrserver_password'), false);
    b.change('language', 'ru'); b.tick(2000);
    server.respond(b.requests.at(-1));
    assert.equal(putBody(b.requests.at(-1)).data.torrserver_password, '', 'another save retains the remote tombstone');
    server.respond(b.requests.at(-1));
    assert.deepEqual(server.data(), {language: 'ru', torrserver_password: ''});
    for (const p of [a, b]) {
      const restarted = acceptanceDevice(server, {items: p.items, hidden: true});
      assert.equal(restarted.items.torrserver_password, '');
      assert.equal(Object.hasOwn(JSON.parse(restarted.items[BASE]), 'torrserver_password'), false);
      restarted.tick(600000);
      assert.equal(restarted.requests.length, 2, 'a successful clear stays clean across reboot');
      assert.deepEqual(restarted.notices, []);
    }
  }
});

test('acceptance: boot and live omission recover previously saved credentials without uploading repeatedly', () => {
  for (const reboot of [false, true]) {
    const server = sharedDocument({language: 'en', torrserver_password: 'old secret'});
    const a = acceptanceDevice(server);
    a.change('torrserver_password', 'saved secret'); a.tick(2000);
    server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
    let b = acceptanceDevice(server);
    const savedEtag = server.etag();
    server.omitEncrypted('torrserver_password');
    if (reboot) b = acceptanceDevice(server, {items: b.items});
    else {
      b.tick(60000); const get = b.requests.at(-1);
      assert.equal(get.config.headers['If-None-Match'], savedEtag);
      server.respond(get);
      assert.equal(server.responses.at(-1), 200, 'unreadable fields must bypass conditional 304');
    }
    assert.equal(b.items.torrserver_password, 'saved secret');
    b.tick(2000); server.respond(b.requests.at(-1));
    const put = b.requests.at(-1);
    assert.equal(put.config.headers['If-Match'], savedEtag, 'recovery PUT keeps the stored-document ETag');
    assert.equal(putBody(put).data.torrserver_password, 'saved secret');
    assert.deepEqual(putBody(put).encrypt, realScope.encrypt);
    server.respond(put);
    assert.equal(JSON.parse(b.items[BASE]).torrserver_password,
      JSON.parse(a.items[BASE]).torrserver_password, 'successful recovery restores the clean saved base');
    assert.equal(server.data().torrserver_password, 'saved secret');
    b.tick(60000); server.respond(b.requests.at(-1));
    assert.equal(server.responses.at(-1), 304);
    b.visibility(true);
    const count = b.requests.length;
    b.tick(600000); assert.equal(b.requests.length, count);
    const restarted = acceptanceDevice(server, {items: b.items, hidden: true});
    restarted.tick(600000); assert.equal(restarted.requests.length, 2);
    assert.equal(restarted.items.torrserver_password, 'saved secret');
  }
});

test('acceptance: simultaneous saves from two devices preserve both edits through a 412 interleaving', () => {
  for (const firstWriter of [0, 1]) {
    const server = sharedDocument({language: 'en', favorite: '{"book":[]}'});
    const devices = [acceptanceDevice(server), acceptanceDevice(server)];
    devices[0].change('language', 'ru');
    devices[1].change('favorite', '{"book":[42]}');
    for (const p of devices) { p.tick(2000); server.respond(p.requests.at(-1)); }
    const winner = devices[firstWriter], retrying = devices[1 - firstWriter];
    // Both PUTs were built from the same revision before either could commit.
    assert.equal(winner.requests.at(-1).config.headers['If-Match'],
      retrying.requests.at(-1).config.headers['If-Match']);
    server.respond(winner.requests.at(-1)); server.respond(retrying.requests.at(-1));
    assert.equal(server.responses.at(-1), 412);
    server.respond(retrying.requests.at(-1)); server.respond(retrying.requests.at(-1));
    assert.deepEqual(server.data(), {language: 'ru', favorite: '{"book":[42]}'});
    winner.tick(60000); server.respond(winner.requests.at(-1));
    for (const p of devices) {
      assert.equal(p.items.language, 'ru');
      assert.deepEqual(clean(p.window.Lampa.Storage.get('favorite')), {book: [42]});
      assert.equal(p.reloads.length, 0); assert.deepEqual(p.notices, []);
    }
    assert.equal(winner.requests.length, 5, 'one save GET/PUT followed by one idle poll');
    assert.equal(retrying.requests.length, 6, 'one save GET/PUT plus exactly one GET/PUT retry');
  }
});

test('acceptance: progress writes and remote bookmarks converge live on the timer and screen return', () => {
  const server = sharedDocument({favorite: '{"book":[]}', file_view: '{"movie":0.1}', torrserver_url: 'http://old'});
  const a = acceptanceDevice(server, {items: {player: 'native', jackett_key: 'a', plugins: '["a"]'}});
  const b = acceptanceDevice(server, {items: {player: 'external', jackett_key: 'b', plugins: '["b"]'}});
  b.change('file_view', '{"movie":0.5}');
  a.change('favorite', '{"book":[42]}'); a.change('torrserver_url', 'http://new'); a.tick(2000);
  server.respond(a.requests.at(-1)); server.respond(a.requests.at(-1));
  b.tick(59999); assert.equal(b.requests.length, 2);
  b.tick(1); server.respond(b.requests.at(-1));
  assert.deepEqual(clean(b.window.Lampa.Storage.get('favorite')), {book: [42]});
  assert.equal(b.items.torrserver_url, 'http://new'); assert.equal(b.items.file_view, '{"movie":0.5}');
  assert.deepEqual(b.refreshes, ['favorite']);
  b.visibility(true); server.respond(b.requests.at(-1)); server.respond(b.requests.at(-1));
  assert.deepEqual(server.data(), {favorite: '{"book":[42]}', file_view: '{"movie":0.5}', torrserver_url: 'http://new'});
  a.visibility(true); a.visibility(false); server.respond(a.requests.at(-1));
  assert.deepEqual(clean(a.window.Lampa.Storage.get('file_view')), {movie: 0.5});
  assert.deepEqual(a.refreshes, ['timeline']);
  for (const [p, player, key] of [[a, 'native', 'a'], [b, 'external', 'b']]) {
    assert.equal(p.items.player, player); assert.equal(p.items.jackett_key, key);
    assert.equal(p.items.plugins, JSON.stringify([key]));
    assert.equal(p.reloads.length, 0); assert.deepEqual(p.notices, []);
  }
});

test('acceptance: temporary whitelist additions alone enable export, import and encrypt metadata', () => {
  for (const scopeName of ['user', 'user_optional']) {
    const scope = clean(realScope), key = 'acceptance_setting';
    scope.scopes[scopeName].push(key); scope.encrypt.push(key);
    const server = sharedDocument({});
    const a = acceptanceDevice(server, {hidden: true}, scope);
    const b = acceptanceDevice(server, {hidden: true}, scope);
    a.change(key, '{ "value": "новое" } '); a.tick(scopeName === 'user' ? 2000 : 300000);
    server.respond(a.requests.at(-1));
    assert.deepEqual(putBody(a.requests.at(-1)).encrypt, scope.encrypt);
    server.respond(a.requests.at(-1));
    assert.equal(server.data()[key], a.items[key]);
    b.visibility(false); server.respond(b.requests.at(-1));
    assert.equal(b.items[key], a.items[key]);
    assert.deepEqual(clean(b.window.Lampa.Storage.get(key)), {value: 'новое'});
    assert.equal(b.reloads.length, 0);
  }
});

test('acceptance: logout and confirmed session expiry mask reserve data while retaining device settings', () => {
  for (const status of [null, 401, 409]) {
    const reserve = {favorite: {book: ['private']}, file_view: {private: 1}, torrserver_password: 'private'};
    const local = {player: 'native', jackett_key: 'local-key', plugins: '["local"]', file_view_cub: 'external'};
    const server = sharedDocument({language: 'en', favorite: '{"book":[42]}',
      ...(status ? {torrserver_password: 'private'} : {})});
    const p = acceptanceDevice(server, {items: local, reserve});
    if (status) {
      p.tick(60000); p.requests.at(-1).fail(status);
      assert.equal(expireCount(p), 1);
    }
    // account.js confirms guest after the expiry event; it owns that recheck.
    p.state(guestState);
    assert.equal(p.reloads.length, 1);
    const guest = page({items: p.reloads[0], reserve, state: guestState, upstreamStorage: true, uninspected: true});
    for (const key of Object.keys(reserve)) {
      assert.equal(guest.items[key], '');
      assert.equal(guest.window.Lampa.Storage.get(key, ''), '', 'the actual upstream getter masks reserve');
    }
    for (const [key, value] of Object.entries(local)) assert.equal(guest.items[key], value);
    assert.equal(guest.items[USER], undefined); assert.equal(guest.items[BASE], undefined);
    guest.tick(600000); assert.equal(guest.requests.length, 0);
  }
});
