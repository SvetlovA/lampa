// Run with: node --test backend/pkg/storage/testdata/torrserver-one-link.test.cjs
// Executes the actual bundle functions without booting Lampa or contacting a server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const bundle = fs.readFileSync(path.join(__dirname, '../../../../app.min.js'), 'utf8');
const marker = 'svtlv_torrserver_legacy_links';

function source(name, indent = '  ') {
  const declaration = indent === '  ' ? '\n' + indent + 'function ' + name + '(' : '\n' + indent + name + ': function ' + name + '(';
  const found = bundle.indexOf(declaration);
  assert.ok(found >= 0, 'bundle function exists: ' + name);
  const start = found + 1 + (indent === '  ' ? 0 : indent.length + name.length + 2);
  const end = bundle.indexOf('\n' + indent + '}', start);
  assert.ok(end > start, 'bundle function ends: ' + name);
  return bundle.slice(start, end + indent.length + 2);
}

function device(values = {}, options = {}) {
  const items = {...values};
  const writes = [];
  const notices = [];
  const localStorage = {
    getItem(key) {
      if (options.readError) throw new Error('storage denied');
      return Object.hasOwn(items, key) ? items[key] : null;
    },
    setItem(key, value) {
      writes.push([key, String(value)]);
      const failed = options.fail && options.fail(key, writes.length);
      if (failed === 'throw') throw new Error('storage full');
      if (failed !== 'silent') items[key] = String(value);
    }
  };
  const context = vm.createContext({
    window: {localStorage, location: {protocol: options.protocol || 'https:'}},
    readed: {}, reserve: options.reserve || {},
    Arrays: {isObject: v => !!v && typeof v === 'object' && !Array.isArray(v), isArray: Array.isArray},
    Noty: {show: message => notices.push(message)},
    Base64: {encode: text => Buffer.from(text).toString('base64')}
  });
  vm.runInContext(source('get$1') + source('checkEmptyUrl'), context);
  context.Storage = {
    get: context.get$1,
    field: key => context.get$1(key),
    value: key => localStorage.getItem(key) || ''
  };
  context.Utils$1 = {checkEmptyUrl: context.checkEmptyUrl};
  vm.runInContext(source('migrateSvtlvTorrserverUrl') + source('ip') + source('url$5'), context);
  return {items, writes, notices, context, options, migrate: context.migrateSvtlvTorrserverUrl};
}

for (const selection of ['one', 'two']) {
  for (const [primary, secondary] of [['http://first:8090', 'https://second:8091'], ['', 'second:8091'], ['first:8090', ''], ['', '']]) {
    test('migration ' + JSON.stringify({selection, primary, secondary}), () => {
      const d = device({torrserver_url: primary, torrserver_url_two: secondary, torrserver_use_link: selection});
      const effective = selection === 'two' ? secondary || primary : primary || secondary;
      assert.equal(d.migrate(), true);
      assert.equal(d.items.torrserver_url, effective);
      assert.equal(d.context.ip(), effective);
      assert.deepEqual(JSON.parse(d.items[marker]), {version: 1, primary, secondary, selection, complete: true});
      assert.equal(d.writes[0][0], marker, 'backup precedes URL mutation');
      assert.equal(d.items.torrserver_url_two, secondary);
      assert.equal(d.items.torrserver_use_link, selection);
      assert.deepEqual(d.notices, []);
      for (const edited of ['https://edited:9000', '']) {
        d.items.torrserver_url = edited;
        delete d.context.readed.torrserver_url;
        const writeCount = d.writes.length;
        assert.equal(d.migrate(), true);
        assert.equal(d.context.ip(), edited, 'later edits and clearing never restore the second URL');
        assert.equal(d.writes.length, writeCount);
      }
    });
  }
}

test('migration includes IndexedDB reserve and invalidates the cached primary', () => {
  const d = device({}, {reserve: {torrserver_url: 'first', torrserver_url_two: 'second', torrserver_use_link: 'two'}});
  assert.equal(d.context.Storage.get('torrserver_url'), 'first');
  assert.equal(d.migrate(), true);
  assert.equal(d.context.ip(), 'second');
});

for (const failedWrite of [1, 2, 3]) {
  for (const failure of ['throw', 'silent']) {
    test('write ' + failedWrite + ' ' + failure + ' fails visibly and recovers without replaying legacy links', () => {
      const d = device({torrserver_url: 'first', torrserver_url_two: 'second', torrserver_use_link: 'two'},
        {fail: (key, count) => count === failedWrite ? failure : false});
      assert.equal(d.migrate(), false);
      assert.equal(d.notices.length, 1);
      assert.equal(d.items.torrserver_url, failedWrite === 3 ? 'second' : 'first');
      if (d.items[marker]) assert.equal(JSON.parse(d.items[marker]).complete, false);
      d.options.fail = null;
      // A reload discards Storage's in-memory cache.
      d.context.readed = {};
      if (failedWrite === 2) {
        assert.equal(d.migrate(), false, 'partial backup requires checking the URL rather than replaying old links');
        assert.equal(d.context.ip(), 'first');
        d.items.torrserver_url = 'second';
        d.context.readed = {};
      }
      assert.equal(d.migrate(), true);
      assert.equal(d.context.ip(), 'second');
      assert.equal(JSON.parse(d.items[marker]).complete, true);
    });
  }
}

test('editing after an interrupted completion wins', () => {
  const d = device({torrserver_url: 'first', torrserver_url_two: 'second', torrserver_use_link: 'two'},
    {fail: (key, count) => count === 3 ? 'throw' : false});
  assert.equal(d.migrate(), false);
  d.options.fail = null;
  d.items.torrserver_url = '';
  d.context.readed = {};
  assert.equal(d.migrate(), true);
  assert.equal(d.context.ip(), '');
});

for (const backup of ['{broken', '{}', 'null', '{"version":2}']) {
  test('invalid migration journal ' + backup + ' fails visibly without modifying URLs', () => {
    const d = device({[marker]: backup, torrserver_url: 'first', torrserver_url_two: 'second', torrserver_use_link: 'two'});
    assert.equal(d.migrate(), false);
    assert.equal(d.items.torrserver_url, 'first');
    assert.equal(d.writes.length, 0);
    assert.equal(d.notices.length, 1);
  });
}

test('unavailable storage does not throw out of migration', () => {
  const d = device({}, {readError: true});
  assert.equal(d.migrate(), false);
  assert.equal(d.notices.length, 1);
});

for (const protocol of ['http:', 'https:']) {
  for (const url of ['host:8090', 'http://host:8090', 'https://host:8090', '']) {
    test('URL scheme is preserved: ' + protocol + ' ' + url, () => {
      const d = device({torrserver_url: url, torrserver_url_two: 'ignored', torrserver_use_link: 'two'}, {protocol});
      assert.equal(d.context.url$5(), url && (url.includes('://') ? url : protocol + '//' + url));
    });
  }
}

test('authenticated requests use the sole URL even when legacy link two is selected', () => {
  const d = device({torrserver_url: 'http://first', torrserver_url_two: 'http://second', torrserver_use_link: 'two',
    torrserver_auth: 'true', torrserver_login: 'user', torrserver_password: 'pass'});
  const headers = [];
  const xhr = {setRequestHeader: (...args) => headers.push(args)};
  d.context.params = {url: 'http://first/torrents'};
  vm.runInContext(source('beforeSend', '        '), d.context);
  d.context.beforeSend(xhr);
  assert.deepEqual(headers, [['Authorization', 'Basic ' + Buffer.from('user:pass').toString('base64')]]);
  headers.length = 0;
  d.context.params.url = 'http://second/torrents';
  d.context.beforeSend(xhr);
  assert.deepEqual(headers, []);
});

test('TorrServer parser uses the sole URL and errors when it is cleared', () => {
  const d = device({torrserver_url: 'http://first', torrserver_url_two: 'http://second', torrserver_use_link: 'two', parser_torrent_type: 'torrserver'});
  const urls = [];
  const failures = [];
  d.context.torrserver = (params, url, done) => { urls.push(url); done({Results: []}); };
  d.context.Lang = {translate: key => key};
  vm.runInContext(source('get$5'), d.context);
  d.context.get$5({}, () => {}, error => failures.push(error));
  assert.deepEqual(urls, ['http://first']);
  d.items.torrserver_url = '';
  d.context.readed = {};
  d.context.get$5({}, () => {}, error => failures.push(error));
  assert.deepEqual(urls, ['http://first']);
  assert.equal(failures.length, 1);
});

test('My Torrents requests the sole URL', () => {
  const d = device({torrserver_url: 'http://first', torrserver_url_two: 'http://second', torrserver_use_link: 'two'});
  const calls = [];
  d.context.clear$d = () => {};
  d.context.network$b = {silent: (url, done, fail, body) => {calls.push([url, JSON.parse(body)]); done([{hash: 'hash'}]);}};
  vm.runInContext(source('my'), d.context);
  let response;
  d.context.my(value => {response = value;}, () => assert.fail('unexpected request failure'));
  assert.deepEqual(calls, [['http://first/torrents', {action: 'list'}]]);
  assert.equal(response[0].hash, 'hash');
});

// Exercise the actual UI hooks with a small jQuery/controller stand-in.
test('speed test and server status hooks use the remaining input and URL', () => {
  const d = device({torrserver_url: 'http://first', torrserver_url_two: 'http://second', torrserver_use_link: 'two',
    torrserver_auth: 'true', torrserver_login: 'user', torrserver_password: 'pass'});
  const settingsHooks = [];
  const changes = [];
  const queries = [];
  const statusCalls = [];
  let enter;
  let speed;
  const item = {find: () => item, removeClass: () => item, addClass: () => item, after: () => {}};
  d.context.$ = selector => {
    queries.push(selector);
    return selector.startsWith('<') ? {on: (name, callback) => {assert.equal(name, 'hover:enter'); enter = callback;}} : item;
  };
  d.context.Settings = {listener: {follow: (name, callback) => settingsHooks.push(callback)}};
  d.context.Storage.listener = {follow: (name, callback) => changes.push(callback)};
  d.context.Torserver = {ip: d.context.ip, url: d.context.url$5};
  d.context.Lang = {translate: key => key};
  d.context.Platform = {is: () => false};
  d.context.start$2 = params => {speed = params;};
  d.context.torrent_net = {clear: () => {}, timeout: () => {}, native: (url, done, fail, data, head) => statusCalls.push([url, head])};
  vm.runInContext(source('init$h') + source('init$b') + source('check'), d.context);
  d.context.init$h();
  d.context.init$b();
  settingsHooks.forEach(hook => hook({name: 'server', body: {}}));
  enter();
  assert.equal(speed.url, 'http://first/download/300');
  assert.equal(speed.login, 'user');
  assert.equal(speed.password, 'pass');
  assert.equal(statusCalls[0][0], 'http://first');
  assert.equal(statusCalls[0][1].headers.Authorization, 'Basic ' + Buffer.from('user:pass').toString('base64'));
  assert.ok(queries.includes('[data-name="torrserver_url"]'));
  assert.ok(queries.every(query => !query.includes('torrserver_url_two')));
  const before = statusCalls.length;
  changes.forEach(hook => hook({name: 'torrserver_url_two'}));
  changes.forEach(hook => hook({name: 'torrserver_use_link', value: 'two'}));
  assert.equal(statusCalls.length, before);
  d.items.torrserver_url = 'http://edited';
  d.context.readed = {};
  changes.forEach(hook => hook({name: 'torrserver_url'}));
  assert.equal(statusCalls.at(-1)[0], 'http://edited');
});

test('migration finishes after reserve loading and before boot consumers', () => {
  const d = device();
  const queue = [];
  let restored;
  d.context.Task = {queue: fn => queue.push(fn), secondary: () => {}, start: () => {}};
  d.context.LoadingProgress = {status: () => {}, step: () => {}};
  d.context.Storage.task = next => {
    d.context.reserve = {torrserver_url: 'first', torrserver_url_two: 'second', torrserver_use_link: 'two'};
    restored = next;
  };
  vm.runInContext(source('loadTask'), d.context);
  d.context.loadTask();
  let advanced = false;
  queue[1](() => {advanced = true; assert.equal(d.context.ip(), 'second');});
  assert.equal(advanced, false);
  assert.equal(d.items[marker], undefined);
  restored();
  assert.equal(advanced, true);
  assert.equal(JSON.parse(d.items[marker]).complete, true);
});

test('search source reacts only to the sole URL and parser toggle', () => {
  const d = device({torrserver_url: 'http://first', torrserver_url_two: 'http://second', torrserver_use_link: 'two', parse_in_search: 'true'});
  const hooks = [];
  const added = [];
  const removed = [];
  d.context.Storage.listener = {follow: (name, callback) => hooks.push(callback)};
  d.context.Storage.set = (key, value) => {d.items[key] = String(value); delete d.context.readed[key];};
  d.context.network$2 = {clear: () => {}};
  d.context.Platform = {is: () => false};
  d.context.Torserver = {url: d.context.url$5};
  d.context.Lang = {translate: key => key};
  d.context.Search = {addSource: value => added.push(value), removeSource: value => removed.push(value)};
  vm.runInContext(source('init$x'), d.context);
  d.context.init$x();
  assert.equal(added.length, 1);
  hooks[0]({name: 'torrserver_url_two'});
  hooks[0]({name: 'torrserver_use_link'});
  assert.equal(removed.length, 0);
  d.items.torrserver_url = '';
  d.context.readed = {};
  hooks[0]({name: 'torrserver_url'});
  assert.equal(removed.length, 1);
  assert.equal(added.length, 1, 'clearing the sole URL disables search without legacy fallback');
});

for (const original of ['first', '']) {
  test('a URL edited back to the original primary after a partial save never restores link two: ' + original, () => {
    const d = device({torrserver_url: original, torrserver_url_two: 'second', torrserver_use_link: 'two'},
      {fail: (key, count) => count === 3 ? 'throw' : false});
    assert.equal(d.migrate(), false);
    d.options.fail = null;
    d.items.torrserver_url = original;
    d.context.readed = {};
    assert.equal(d.migrate(), false, 'incomplete migration remains visible');
    assert.equal(d.context.ip(), original, 'edited or cleared URL wins');
  });
}
