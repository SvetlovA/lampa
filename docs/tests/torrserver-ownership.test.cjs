// Run with: node --test docs/tests/torrserver-ownership.test.cjs
// Execute the actual add-on with controlled public APIs, network and storage.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../../svtlv/torrserver-ownership.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../../index.html'), 'utf8');
const bundle = fs.readFileSync(path.join(__dirname, '../../app.min.js'), 'utf8');
const ipSource = bundle.match(/  function ip\(\) \{[\s\S]*?\n  \}/)[0];
const key = 'svtlv_torrserver_guest_hashes';
const X = 'a'.repeat(40), Y = 'b'.repeat(40), Z = 'c'.repeat(64);
const id = url => crypto.createHash('sha256').update(url.trim().toLowerCase().replace(/^https?:\/\//, '').replace(/\/+$/, '')).digest('hex');
const guest = {status: 'guest', generation: 1, user_id: ''};
const account = (user = 'Alice', generation = 1) => ({status: 'signed_in', generation, user_id: user});
const clean = value => JSON.parse(JSON.stringify(value));

function page(options = {}) {
  const calls = [], requests = [], notices = [], timers = [], events = [], replacements = [];
  const listeners = {}, handlers = {};
  const items = options.items || {};
  const settings = {torrserver_url: 'https://server:8090/', torrserver_url_two: '', torrserver_use_link: 'one',
    language: 'en', torrserver_savedb: false, ...options.settings};
  let activity = options.activity || null;
  const faults = {read: false, write: false, ...options.faults};
  const listener = {
    follow(name, fn) { (listeners[name] ||= []).push(fn); },
    send(name, event) {
      events.push({name, event});
      for (const fn of listeners[name] || []) fn(event);
    }
  };
  const Lampa = {
    Torserver: {},
    Storage: {get: name => settings[name],
      field: name => name === 'torrserver_savedb' ? settings[name] === true || settings[name] === 'true' : settings[name], listener},
    Listener: listener,
    Noty: {show: text => notices.push(text)},
    Activity: {
      active: () => activity,
      all: () => options.stack || [],
      replace() { replacements.push(activity); activity = {...activity, revision: replacements.length}; }
    },
    Utils: {hash() { throw new Error('short hashes must not be used'); }}
  };
  Lampa.Torserver.ip = vm.runInNewContext(ipSource + '\nip;', {Storage: Lampa.Storage});
  for (const name of ['my', 'add', 'hash', 'remove', 'drop']) {
    Lampa.Torserver[name] = function (...args) {
      const operation = {
        name, args, receiver: this,
        success(...result) { args[name === 'my' ? 0 : 1]?.apply(operation, result); },
        fail(...result) { args[name === 'my' ? 1 : 2]?.apply(operation, result); }
      };
      calls.push(operation);
      if (options.original) options.original(operation);
      return operation;
    };
  }
  const window = {
    location: {protocol: options.protocol || 'https:'},
    localStorage: {
      getItem(name) { if (faults.read) throw new Error('read'); return items[name] ?? null; },
      setItem(name, value) { if (faults.write) throw new Error('write'); if (!faults.silentWrite) items[name] = value; }
    }
  };
  if (options.state !== null) window.svtlv_account_state = options.state || guest;
  if (!options.lateLampa) window.Lampa = Lampa;
  function $() { return {on: (name, fn) => { handlers[name] = fn; }}; }
  $.ajax = config => {
    if (options.ajaxThrows) throw new Error('ajax');
    const request = {
      config,
      success(body, status = config.type === 'GET' ? 200 : 204) { config.success(body, 'success', {status}); },
      fail(status = 503) { config.error({status}); }
    };
    requests.push(request);
    return request;
  };
  const context = vm.createContext({window, $, setTimeout: fn => timers.push(fn)});
  vm.runInContext(source, context);
  return {
    window, Lampa, calls, requests, notices, settings, items, faults, events, replacements, context,
    install() { window.Lampa = Lampa; },
    reload() { vm.runInContext(source, context); },
    setState(state) { window.svtlv_account_state = state; listener.send('svtlv_account', {type: 'state', state}); },
    change(name, value) { settings[name] = value; listener.send('change', {name, value}); },
    changeURL(url) { this.change('torrserver_url', url); },
    select(link) { this.change('torrserver_use_link', link); },
    navigate(next) { activity = next; },
    storageEvent(name = key) { handlers.storage({originalEvent: {key: name}}); },
    flush() { const pending = timers.splice(0); for (const fn of pending) fn(); },
    seed(hashes, url = settings.torrserver_url) { items[key] = JSON.stringify({version: 1, servers: {[id(url)]: hashes}}); },
    hashes(url = settings.torrserver_url) { return JSON.parse(items[key]).servers[id(url)] || []; },
    list() {
      const success = [], fail = [];
      Lampa.Torserver.my((...args) => success.push(clean(args)), (...args) => fail.push(args));
      return {success, fail};
    }
  };
}

function switchAwayAndBack(p, name = 'torrserver_use_link') {
  const original = p.settings[name];
  const endpoint = p.Lampa.Torserver.ip();
  p.change(name, name === 'torrserver_use_link' ? (original === 'two' ? 'one' : 'two') : 'http://different');
  assert.notEqual(p.Lampa.Torserver.ip(), endpoint, 'the intermediate change reaches a different server');
  p.change(name, original);
  assert.equal(p.Lampa.Torserver.ip(), endpoint);
}

test('installs before readiness and protects synchronous first use at API publication', () => {
  const p = page({lateLampa: true, state: null});
  p.install();
  const result = p.list();
  assert.equal(result.fail.length, 1);
  assert.equal(p.calls.length, 0);
  assert.equal(p.requests.length, 0);
  assert.equal(p.notices.length, 1);
  assert.equal(Object.getOwnPropertyDescriptor(p.window, 'Lampa').value, p.Lampa);
  p.reload();
  p.setState(guest);
  p.seed([X]);
  p.list();
  assert.equal(p.calls.length, 1, 'load guard avoids double wrapping');
});

test('the actual bundle API publication installs wrappers before its next statement', () => {
  const p = page({lateLampa: true, state: null});
  const init = bundle.match(/  function initClass\(\) \{[\s\S]*?\n  \}/)[0];
  for (const match of init.matchAll(/^\s+(\w+): ([\w$]+)/gm)) {
    p.context[match[2]] = p.Lampa[match[1]] || {};
  }
  p.context.Subscribe$2 = () => p.Lampa.Listener;
  p.context.Request = function () {};
  vm.runInContext(init + '\ninitClass(); window.Lampa.Torserver.my();', p.context);
  assert.equal(p.calls.length, 0, 'unresolved first render never requests the shared list');
  assert.equal(p.notices.length, 1);
  p.setState(guest);
  p.seed([X]);
  p.window.Lampa.Torserver.my();
  assert.equal(p.calls.length, 1);
});

test('polling handles an incomplete public API without assuming guest identity', () => {
  const p = page({lateLampa: true, state: null});
  p.window.Lampa = {};
  p.flush();
  p.window.Lampa = p.Lampa;
  p.flush();
  assert.equal(p.list().fail.length, 1);
  assert.equal(p.calls.length, 0);
});

test('late installation replaces an already active My Torrents activity', () => {
  const p = page({activity: {component: 'mytorrents'}});
  p.flush();
  assert.equal(p.replacements.length, 1);
  p.setState(account());
  p.flush();
  assert.equal(p.replacements.length, 2);
  p.changeURL('http://another:8090');
  p.flush();
  assert.equal(p.replacements.length, 3);
  p.navigate({component: 'full'});
  p.setState(guest);
  p.flush();
  assert.equal(p.replacements.length, 3);
});

for (const name of ['torrserver_url', 'torrserver_url_two', 'torrserver_use_link']) {
  test('connection changes refresh My Torrents and discard retained lists: ' + name, () => {
    const stack = [];
    const p = page({activity: {component: 'mytorrents'}, stack});
    p.flush();
    let destroyed = 0;
    const retained = {component: 'mytorrents', activity: {destroy() { destroyed++; }}};
    stack.push(retained);
    p.change(name, name === 'torrserver_use_link' ? 'two' : 'http://other');
    assert.equal(destroyed, 1);
    assert.equal(retained.activity, null);
    p.flush();
    assert.equal(p.replacements.length, 2);
    p.navigate({component: 'full'});
    p.change(name, '');
    p.flush();
    assert.equal(p.replacements.length, 2, 'details stay open');
  });
}

for (const protocol of ['file:', 'app:']) test('non-HTTP origin leaves all original methods intact: ' + protocol, () => {
  const p = page({protocol});
  p.Lampa.Torserver.remove(X);
  assert.equal(p.calls[0].name, 'remove');
  assert.equal(p.window.svtlv_torrserver_loaded, undefined);
});

for (const state of [null, {status: 'checking', generation: 1}, {status: 'unavailable', generation: 2}]) {
  test('unresolved identity blocks lists and mutations: ' + JSON.stringify(state), () => {
    const p = page({state});
    assert.equal(p.list().fail.length, 1);
    for (const method of ['add', 'remove']) {
      let failed = 0;
      p.Lampa.Torserver[method](method === 'remove' ? X : {}, undefined, () => failed++);
      assert.equal(failed, 1);
    }
    assert.equal(p.calls.length, 0);
    assert.equal(p.requests.length, 0);
  });
}

for (const state of [null, {status: 'checking', generation: 1}, {status: 'unavailable', generation: 2}]) {
  for (const saved of [false, true]) test('unresolved identity permits playback without membership: ' + JSON.stringify(state) + '/' + saved, () => {
    const p = page({state, settings: {torrserver_savedb: saved}});
    const object = {link: 'magnet:test'};
    let response, receiver;
    p.Lampa.Torserver.hash(object, function (...args) { response = args; receiver = this; });
    assert.equal(p.calls.length, 1, 'playback reaches TorrServer directly');
    assert.equal(p.calls[0].args[0], object);
    assert.equal(p.calls[0].receiver, p.Lampa.Torserver);
    // A resolved session must not assign an earlier unresolved playback to it.
    p.setState(account('Bob', 3));
    p.calls[0].success({hash: X}, 'success');
    assert.deepEqual(response, [{hash: X}, 'success']);
    assert.equal(receiver, p.calls[0]);
    assert.equal(p.requests.length, 0);
    assert.equal(p.items[key], undefined);
    assert.equal(p.notices.length, 0);
  });
}

for (const next of [{status: 'checking', generation: 2}, {status: 'unavailable', generation: 2}, guest, account('Bob', 2)]) {
  test('session changes during playback preserve callbacks without recording membership: ' + next.status, () => {
    for (const failed of [false, true]) {
      const p = page({state: account(), settings: {torrserver_savedb: true}});
      let success, fail, receiver;
      p.Lampa.Torserver.hash({}, function (...args) { success = args; receiver = this; },
        function (...args) { fail = args; receiver = this; });
      p.setState(next);
      if (failed) p.calls[0].fail('timeout', {code: 1});
      else p.calls[0].success({hash: X});
      assert.deepEqual(success, failed ? undefined : [{hash: X}]);
      assert.deepEqual(fail, failed ? ['timeout', {code: 1}] : undefined);
      assert.equal(receiver, p.calls[0]);
      assert.equal(p.requests.length, 0);
      assert.equal(p.items[key], undefined);
    }
  });
}

for (const url of ['abc', 'https://SERVER:8090///', ' http://server:8090/ ', 'https://сервер/каталог/',
  'https://host/' + 'a'.repeat(50), 'https://host/' + 'a'.repeat(120), 'https://host:443/path?a=b']) {
  test('server ID matches Node SHA-256 and ignores page scheme: ' + url, () => {
    const p = page({state: account(), settings: {torrserver_url: url}});
    p.list();
    assert.equal(p.requests[0].config.url, '/api/v1/torrservers/' + id(url) + '/hashes');
    const q = page({protocol: 'http:', state: account(), settings: {torrserver_url: url.replace(/^https:/, 'http:')}});
    q.list();
    assert.equal(q.requests[0].config.url, p.requests[0].config.url);
  });
}

test('distinct servers that collide under Lampa.Utils.hash remain separated', () => {
  const short = text => Math.abs([...text].reduce((sum, char) => (sum * 31 + char.charCodeAt(0)) | 0, 0));
  const urls = ['http://server/an', 'http://server/c0'];
  assert.equal(short('server/an'), short('server/c0'));
  const ids = urls.map(url => {
    const p = page({state: account(), settings: {torrserver_url: url}});
    p.list();
    return p.requests[0].config.url;
  });
  assert.notEqual(ids[0], ids[1]);
});

for (const first of ['membership', 'torrent']) test('account list waits for both sources: ' + first, () => {
  const p = page({state: account()});
  const result = p.list();
  assert.equal(p.requests.length, 1);
  assert.equal(p.calls.length, 1, 'original starts immediately, without waiting for GET');
  assert.equal(p.calls[0].receiver, p.Lampa.Torserver);
  const membership = () => p.requests[0].success({hashes: [X, Z]});
  const torrents = () => p.calls[0].success([{hash: X.toUpperCase()}, {hash: Y}, {hash: Z}, {hash: 'bad'}, {}]);
  (first === 'membership' ? membership : torrents)();
  assert.equal(result.success.length, 0);
  (first === 'membership' ? torrents : membership)();
  assert.deepEqual(result.success, [[[{hash: X.toUpperCase()}, {hash: Z}]]]);
  assert.equal(result.fail.length, 0);
  assert.equal(p.requests[0].config.timeout, 15000);
  assert.equal(p.requests[0].config.cache, false);
});

for (const data of [null, {}, {hashes: 'bad'}, {hashes: [X, 'invalid']}]) test('malformed account inventory fails closed: ' + JSON.stringify(data), () => {
  const p = page({state: account()});
  const result = p.list();
  p.requests[0].success(data);
  p.calls[0].success([{hash: X}, {hash: Y}]);
  assert.equal(result.success.length, 0);
  assert.equal(result.fail.length, 1);
  assert.equal(p.notices.length, 1);
});

test('account API failure never falls back to shared or guest data', () => {
  const p = page({state: account()});
  p.seed([X]);
  const result = p.list();
  p.requests[0].fail();
  p.calls[0].success([{hash: X}]);
  assert.equal(result.success.length, 0);
  assert.equal(result.fail.length, 1);
});

for (const operation of ['my', 'add', 'remove']) test('shared cookie switch rejects stale tab without retry: ' + operation, () => {
  const p = page({state: account('Alice')});
  const result = {success: 0, fail: 0};
  const success = () => result.success++;
  const fail = () => result.fail++;
  if (operation === 'my') p.Lampa.Torserver.my(success, fail);
  else p.Lampa.Torserver[operation](operation === 'add' ? {} : X, success, fail);
  if (operation === 'add') p.calls[0].success({hash: X});
  assert.equal(p.requests[0].config.headers['X-Lampa-Expected-User'], 'Alice');
  // the API detects Bob's shared cookie against Alice's captured precondition
  p.requests[0].fail(409);
  if (operation === 'my') p.calls[0].success([{hash: Y}]);
  assert.deepEqual(result, {success: 0, fail: 1});
  assert.equal(p.window.svtlv_account_state.status, 'checking');
  assert.ok(p.events.some(item => item.event.type === 'expired'));
  assert.equal(p.items[key], undefined);
  assert.equal(p.requests.length, 1, 'never replay a mutation under the new identity');
  p.setState(account('Bob', 3));
  p.list();
  assert.equal(p.requests[1].config.headers['X-Lampa-Expected-User'], 'Bob');
});

for (const change of ['logout', 'account', 'URL', 'guest storage']) test('scope changes discard retained My Torrents before Back: ' + change, () => {
  const stack = [];
  const detail = {component: 'full'};
  const p = page({state: change === 'guest storage' ? guest : account(), activity: detail, stack});
  p.flush();
  let destroyed = 0;
  const retained = {component: 'mytorrents', activity: {destroy() { destroyed++; this.cards = []; }, cards: [X]}};
  const oldSlide = retained.activity;
  stack.push(retained, detail);
  if (change === 'logout') p.setState({...guest, generation: 2});
  if (change === 'account') p.setState(account('Bob', 2));
  if (change === 'URL') p.changeURL('http://another-server');
  if (change === 'guest storage') p.storageEvent();
  assert.equal(destroyed, 1);
  assert.equal(retained.activity, null, 'Activity.backward must recreate the component');
  assert.deepEqual(oldSlide.cards, [], 'old cards disappear before any next start');
  p.flush();
  assert.equal(p.replacements.length, 0, 'current details remain open');
  // Exercise the actual bundle Back path: null retained activities are recreated
  // before start/render, rather than resuming the previous account's component.
  let list;
  detail.activity = {destroy() {}};
  Object.assign(p.context, {
    activites: stack, listener$3: {send() {}}, slides: {children: []}, maxsave: 5,
    Lampa: p.Lampa, pushState() {},
    create$4(object) {
      assert.equal(object, retained);
      assert.equal(object.activity, null);
      object.activity = {cards: []};
      p.navigate(object);
      list = p.list();
    },
    start$4(object) {
      assert.deepEqual(object.activity.cards, [], 'old cards cannot resume while lookup is pending');
      p.navigate(object);
    }
  });
  const backward = bundle.match(/  function backward\(\) \{[\s\S]*?\n  \}/)[0];
  vm.runInContext(backward + '\nbackward();', p.context);
  assert.ok(list, 'Back fetches the new scope');
  if (change === 'account' || change === 'URL') p.requests[0].success({hashes: [Y]});
  else p.seed([]);
  p.calls[0].success([{hash: X}, {hash: Y}]);
  if (change === 'account' || change === 'URL') assert.deepEqual(list.success, [[[{hash: Y}]]]);
  else assert.equal(list.fail.length, 1);
});

test('expired list blocks immediately, emits expiry, then uses a confirmed guest independently', () => {
  const p = page({state: account(), activity: {component: 'mytorrents'}});
  p.seed([Y]);
  const before = p.list();
  p.requests[0].fail(401);
  assert.equal(p.window.svtlv_account_state.status, 'checking');
  assert.ok(p.events.some(item => item.event.type === 'expired'));
  p.calls[0].success([{hash: X}, {hash: Y}]);
  assert.equal(before.success.length, 0);
  assert.equal(before.fail.length, 1);
  assert.equal(p.list().fail.length, 1);
  p.setState({...guest, generation: 3});
  const after = p.list();
  p.calls.at(-1).success([{hash: X}, {hash: Y}]);
  assert.deepEqual(after.success, [[[{hash: Y}]]]);
  p.flush();
  assert.ok(p.replacements.length);
});

test('late lists and 401s from an earlier generation cannot affect a new identity', () => {
  const p = page({state: account()});
  const old = p.list();
  p.setState({...guest, generation: 2});
  p.setState(account('Bob', 3));
  p.requests[0].fail(401);
  p.calls[0].success([{hash: X}]);
  assert.equal(p.window.svtlv_account_state.user_id, 'Bob');
  assert.equal(old.success.length + old.fail.length, 0);
  assert.equal(p.notices.length, 0);
  const next = p.list();
  p.requests[1].success({hashes: [Y]});
  p.calls[1].success([{hash: X}, {hash: Y}]);
  assert.deepEqual(next.success, [[[{hash: Y}]]]);
});

test('URL changes drop pending responses and restoring the URL restores its memberships', () => {
  const p = page();
  p.seed([X]);
  const old = p.list();
  p.changeURL('http://different/');
  p.calls[0].success([{hash: X}]);
  assert.equal(old.success.length, 0);
  const other = p.list();
  p.calls[1].success([{hash: X}]);
  assert.equal(other.fail.length, 1);
  p.changeURL('http://server:8090');
  const restored = p.list();
  p.calls[2].success([{hash: X}]);
  assert.equal(restored.success.length, 1);
});

for (const state of [guest, account()]) {
  for (const [one, two, selection, effective] of [
    ['http://primary', 'https://secondary/path', 'one', 'http://primary'],
    ['http://primary', 'https://secondary/path', 'two', 'https://secondary/path'],
    ['http://primary', 'http://secondary', undefined, 'http://primary'],
    ['http://primary', 'http://secondary', 'invalid', 'http://primary'],
    ['', 'http://secondary', 'one', 'http://secondary'],
    ['http://primary', '', 'two', 'http://primary'],
    ['', 'http://secondary', 'invalid', 'http://secondary']
  ]) {
    test('membership follows the actual bundle endpoint: ' + state.status + '/' + [one, two, selection].join('/'), () => {
      const p = page({state, settings: {torrserver_url: one, torrserver_url_two: two, torrserver_use_link: selection}});
      p.seed([X], effective);
      const result = p.list();
      if (state.status === 'signed_in') {
        assert.equal(p.requests[0].config.url, '/api/v1/torrservers/' + id(effective) + '/hashes');
        p.requests[0].success({hashes: [X]});
      }
      p.calls[0].success([{hash: X}, {hash: Y}]);
      assert.deepEqual(result.success, [[[{hash: X}]]]);
      p.Lampa.Torserver.remove(X);
      if (state.status === 'signed_in') {
        assert.equal(p.requests[1].config.url, '/api/v1/torrservers/' + id(effective) + '/hashes/' + X);
        assert.equal(p.requests[1].config.type, 'DELETE');
        p.requests[1].success('');
      } else assert.deepEqual(p.hashes(effective), []);
      assert.equal(p.calls.some(call => call.name === 'remove'), false);
      assert.equal(p.settings.torrserver_url, one);
      assert.equal(p.settings.torrserver_url_two, two);
    });
  }

  test('switching to an empty link retains the effective server and its pending list: ' + state.status, () => {
    const p = page({state});
    p.seed([X]);
    const result = p.list();
    p.select('two');
    if (state.status === 'signed_in') p.requests[0].success({hashes: [X]});
    p.calls[0].success([{hash: X}]);
    assert.deepEqual(result.success, [[[{hash: X}]]]);
  });

  for (const first of state.status === 'signed_in' ? ['membership', 'torrent', 'neither'] : ['neither']) {
    test('switching servers ignores delayed list responses: ' + state.status + '/' + first, () => {
      const p = page({state, settings: {torrserver_url_two: 'http://other'}});
      p.seed([X]);
      const result = p.list();
      if (first === 'membership' && state.status === 'signed_in') p.requests[0].success({hashes: [X]});
      if (first === 'torrent') p.calls[0].success([{hash: X}]);
      p.select('two');
      if (first !== 'membership' && state.status === 'signed_in') p.requests[0].success({hashes: [X]});
      if (first !== 'torrent') p.calls[0].success([{hash: X}]);
      assert.equal(result.success.length + result.fail.length, 0);
      if (state.status === 'signed_in') p.requests[0].fail(401);
      assert.equal(p.window.svtlv_account_state.status, state.status);
      assert.equal(p.notices.length, 0);
    });
  }

  for (const method of ['add', 'hash']) for (const failed of [false, true]) {
    test('switching servers before a torrent response cannot record membership: ' + state.status + '/' + method + '/' + failed, () => {
      const p = page({state, settings: {torrserver_url_two: 'http://other', torrserver_savedb: true}});
      let success = 0, fail = 0;
      p.Lampa.Torserver[method]({}, () => success++, () => fail++);
      p.select('two');
      if (failed) p.calls[0].fail('timeout');
      else p.calls[0].success({hash: X});
      assert.equal(success, Number(method === 'hash' && !failed));
      assert.equal(fail, Number(method === 'hash' && failed));
      assert.equal(p.requests.length, 0);
      assert.equal(p.items[key], undefined);
      assert.equal(p.notices.length, 0);
    });
  }
}

for (const method of ['add', 'hash', 'remove']) for (const failed of [false, true]) {
  test('switching servers ignores pending membership acknowledgements: ' + method + '/' + failed, () => {
    const p = page({state: account(), settings: {torrserver_url_two: 'http://other', torrserver_savedb: true}});
    let success = 0, fail = 0;
    p.Lampa.Torserver[method](method === 'remove' ? X : {}, () => success++, () => fail++);
    if (method !== 'remove') p.calls[0].success({hash: X});
    assert.equal(p.requests[0].config.url, '/api/v1/torrservers/' + id(p.settings.torrserver_url) + '/hashes/' + X);
    p.select('two');
    if (failed) p.requests[0].fail(401);
    else p.requests[0].success('');
    assert.equal(success, Number(method === 'hash'), 'playback already started without waiting for membership');
    assert.equal(fail, 0);
    assert.equal(p.window.svtlv_account_state.status, 'signed_in');
    assert.equal(p.notices.length, 0);
    assert.equal(p.calls.some(call => call.name === 'remove'), false);
  });
}

for (const state of [guest, account()]) {
  for (const name of ['torrserver_use_link', 'torrserver_url', 'torrserver_url_two']) {
    for (const first of state.status === 'signed_in' ? ['membership', 'torrent'] : ['torrent']) {
      test('switching away and back cannot revive delayed lists: ' + state.status + '/' + name + '/' + first, () => {
        const p = page({state, settings: {torrserver_url_two: 'http://other',
          torrserver_use_link: name === 'torrserver_url_two' ? 'two' : 'one'}});
        p.seed([X], p.Lampa.Torserver.ip());
        const result = p.list();
        switchAwayAndBack(p, name);
        const membership = () => { if (state.status === 'signed_in') p.requests[0].success({hashes: [X]}); };
        const torrent = () => p.calls[0].success([{hash: X}]);
        (first === 'membership' ? membership : torrent)();
        (first === 'membership' ? torrent : membership)();
        assert.equal(result.success.length + result.fail.length, 0);
        assert.equal(p.notices.length, 0);
        if (state.status === 'guest') p.seed([Y], p.Lampa.Torserver.ip());
        const fresh = p.list();
        if (state.status === 'signed_in') p.requests[1].success({hashes: [Y]});
        p.calls.at(-1).success([{hash: X}, {hash: Y}]);
        assert.deepEqual(fresh.success, [[[{hash: Y}]]]);
      });
    }
    if (state.status === 'signed_in') test('switching away and back discards stale account-list 401s: ' + name, () => {
      const p = page({state: account(), settings: {torrserver_url_two: 'http://other',
        torrserver_use_link: name === 'torrserver_url_two' ? 'two' : 'one'}});
      const result = p.list();
      switchAwayAndBack(p, name);
      p.requests[0].fail(401);
      p.calls[0].success([{hash: X}]);
      assert.equal(p.window.svtlv_account_state.status, 'signed_in');
      assert.equal(result.success.length + result.fail.length, 0);
      assert.equal(p.events.some(item => item.event.type === 'expired'), false);
      assert.equal(p.notices.length, 0);
    });
  }

  for (const method of ['add', 'hash']) for (const failed of [false, true]) {
    test('switching away and back before a torrent callback cannot record membership: ' + state.status + '/' + method + '/' + failed, () => {
      const p = page({state, settings: {torrserver_url_two: 'http://other', torrserver_savedb: true}});
      let success = 0, fail = 0;
      p.Lampa.Torserver[method]({}, () => success++, () => fail++);
      switchAwayAndBack(p);
      if (failed) p.calls[0].fail('timeout');
      else p.calls[0].success({hash: X});
      assert.equal(success, Number(method === 'hash' && !failed));
      assert.equal(fail, Number(method === 'hash' && failed));
      assert.equal(p.requests.length, 0);
      assert.equal(p.items[key], undefined);
      assert.equal(p.notices.length, 0);
    });
  }

  for (const change of ['inactive URL', 'normalized URL', 'normalized selected alias']) {
    test('same-effective connection changes retain pending lists: ' + state.status + '/' + change, () => {
      const p = page({state, settings: {torrserver_url_two: ' HTTP://SERVER:8090/// '}});
      p.seed([X]);
      const result = p.list();
      if (change === 'inactive URL') p.change('torrserver_url_two', 'http://other');
      if (change === 'normalized URL') p.changeURL(' HTTP://SERVER:8090/// ');
      if (change === 'normalized selected alias') p.select('two');
      if (state.status === 'signed_in') p.requests[0].success({hashes: [X]});
      p.calls[0].success([{hash: X}]);
      assert.deepEqual(result.success, [[[{hash: X}]]]);
    });
  }
}

for (const method of ['add', 'hash', 'remove']) for (const status of [204, 401, 503]) {
  test('switching away and back ignores pending membership acknowledgements: ' + method + '/' + status, () => {
    const p = page({state: account(), settings: {torrserver_url_two: 'http://other', torrserver_savedb: true}});
    let success = 0, fail = 0;
    p.Lampa.Torserver[method](method === 'remove' ? X : {}, () => success++, () => fail++);
    if (method !== 'remove') p.calls[0].success({hash: X});
    assert.equal(p.requests.length, 1);
    switchAwayAndBack(p);
    if (status === 204) p.requests[0].success('');
    else p.requests[0].fail(status);
    assert.equal(success, Number(method === 'hash'), 'playback already started without waiting for membership');
    assert.equal(fail, 0);
    assert.equal(p.window.svtlv_account_state.status, 'signed_in');
    assert.equal(p.notices.length, 0);
    assert.equal(p.calls.some(call => call.name === 'remove'), false);
  });
}

test('membership uses the public resolver rather than independently reading configured fields', () => {
  const p = page({state: account()});
  p.Lampa.Torserver.ip = () => 'https://effective/path';
  p.list();
  assert.equal(p.requests[0].config.url, '/api/v1/torrservers/' + id('https://effective/path') + '/hashes');
});

for (const resolver of [undefined, () => { throw new Error('unavailable'); }, () => null]) {
  test('an unavailable endpoint resolver leaves ownership unconfigured and playback usable: ' + String(resolver), () => {
    const p = page({state: account(), settings: {torrserver_savedb: true}});
    p.Lampa.Torserver.ip = resolver;
    const result = p.list();
    p.calls[0].success([{hash: X}]);
    assert.equal(result.success.length, 0);
    assert.equal(result.fail.length, 1);
    let played = 0, removed = 0;
    p.Lampa.Torserver.hash({}, () => played++);
    p.calls[1].success({hash: X});
    p.Lampa.Torserver.remove(X, () => removed++);
    assert.equal(played, 1);
    assert.equal(removed, 0);
    assert.equal(p.requests.length, 0);
    assert.equal(p.items[key], undefined);
    assert.equal(p.calls.some(call => call.name === 'remove'), false);
  });
}

test('guest lists are profile-local; removal preserves other profiles and server torrents', () => {
  const a = page(), b = page();
  a.seed([X]); b.seed([X, Y]);
  a.Lampa.Torserver.remove(X);
  assert.deepEqual(a.hashes(), []);
  assert.deepEqual(b.hashes(), [X, Y]);
  assert.equal(a.calls.length, 0);
  assert.equal(a.requests.length, 0);
  const result = b.list();
  b.calls[0].success([{hash: X}, {hash: Y}, {hash: Z}]);
  assert.deepEqual(result.success, [[[{hash: X}, {hash: Y}]]]);
});

test('shared-server acceptance flow isolates accounts, guests and retained memberships', () => {
  // Controlled external services; every client runs the actual ownership add-on.
  const memberships = new Map(), servers = new Map();
  const clients = [account('Alice'), account('Bob'), guest, guest].map(state =>
    page({state, settings: {torrserver_url_two: 'http://other-server:8090'}}));
  const [a, b, g, otherGuest] = clients;
  const cursors = new Map(clients.map(client => [client, {calls: 0, requests: 0}]));
  const server = client => {
    const name = id(client.Lampa.Torserver.ip());
    if (!servers.has(name)) servers.set(name, new Set());
    return servers.get(name);
  };
  function settle(client) {
    const cursor = cursors.get(client);
    while (cursor.calls < client.calls.length) {
      const op = client.calls[cursor.calls++];
      if (op.name === 'my') {
        const hashes = [...server(client)];
        if (hashes.length) op.success(hashes.map(hash => ({hash})));
        else op.fail(); // Upstream cannot distinguish empty from unavailable.
      } else {
        assert.equal(op.name, 'add', 'no physical removal request may reach TorrServer');
        const hash = op.args[0].hash;
        server(client).add(hash);
        op.success({hash});
      }
    }
    while (cursor.requests < client.requests.length) {
      const request = client.requests[cursor.requests++];
      const match = /^\/api\/v1\/torrservers\/([a-f0-9]{64})\/hashes(?:\/([a-f0-9]{40}|[a-f0-9]{64}))?$/.exec(request.config.url);
      assert.ok(match);
      const owner = client.window.svtlv_account_state.user_id + '/' + match[1];
      if (!memberships.has(owner)) memberships.set(owner, new Set());
      const hashes = memberships.get(owner);
      if (request.config.type === 'GET') request.success({hashes: [...hashes].sort()});
      else {
        assert.equal(request.config.headers['X-Lampa-Csrf'], '1');
        if (request.config.type === 'PUT') hashes.add(match[2]);
        else {
          assert.equal(request.config.type, 'DELETE');
          hashes.delete(match[2]);
        }
        request.success('');
      }
    }
  }
  function add(client, hash) {
    let saved = false;
    client.Lampa.Torserver.add({hash}, () => { saved = true; });
    settle(client);
    assert.equal(saved, true);
  }
  function visible(client, hashes) {
    const result = client.list();
    settle(client);
    assert.deepEqual(result.success, hashes.length ? [[hashes.map(hash => ({hash}))]] : []);
    assert.equal(result.fail.length, hashes.length ? 0 : 1);
  }
  function remove(client, hash) {
    let removed = false;
    client.Lampa.Torserver.remove(hash, () => { removed = true; });
    settle(client);
    assert.equal(removed, true);
    assert.equal(server(client).has(hash), true, 'membership removal preserves the shared torrent');
  }

  add(a, X); add(b, X); add(b, Y);
  visible(a, [X]); visible(b, [X, Y]); visible(g, []); visible(otherGuest, []);
  add(g, X);
  visible(g, [X]); visible(otherGuest, []);
  remove(g, X);
  visible(g, []); visible(a, [X]); visible(b, [X, Y]);
  add(g, X);

  a.select('two');
  visible(a, []);
  add(a, Z);
  visible(a, [Z]);
  g.select('two');
  visible(g, []);
  add(g, Y);
  visible(g, [Y]); visible(a, [Z]);
  remove(a, Z);
  visible(a, []); visible(g, [Y]);
  a.select('one');
  g.select('one');
  visible(a, [X]);
  visible(g, [X]);
  assert.ok(memberships.get('Alice/' + id('server:8090')).has(X), 'existing primary rows retain their IDs');
  g.select('two');
  visible(g, [Y]);
  g.select('one');
  remove(a, X);
  visible(a, []); visible(b, [X, Y]);
  remove(b, X); // Last account row also leaves X on TorrServer.
  visible(b, [Y]);

  add(a, X); add(b, X); add(g, X);
  remove(a, X);
  server(b).delete(X); // Operator deletion changes only the external list.
  visible(a, []); visible(b, [Y]); visible(g, []);
  add(b, X);
  visible(a, []); visible(b, [Y, X]); visible(g, [X]); visible(otherGuest, []);
  assert.deepEqual(g.hashes(), [X]);
  assert.equal(g.requests.length + otherGuest.requests.length, 0, 'guest membership stays entirely local');
});

test('the same configured endpoint has one account server ID across HTTP and HTTPS page origins', () => {
  const http = page({protocol: 'http:', state: account(), settings: {torrserver_url: ' SERVER:8090/ '}});
  const https = page({protocol: 'https:', state: account(), settings: {torrserver_url: 'https://server:8090///'}});
  for (const client of [http, https]) client.list();
  assert.equal(http.requests[0].config.url, https.requests[0].config.url);
  assert.equal(http.requests[0].config.url, '/api/v1/torrservers/' + id('server:8090') + '/hashes');
});

for (const raw of ['', 'invalid', 'null', '{}', '{"version":2,"servers":{}}',
  '{"version":1,"servers":[]}', '{"version":1,"servers":{"bad":[]}}',
  JSON.stringify({version: 1, servers: {[id('server:8090')]: [X.toUpperCase()]}}),
  JSON.stringify({version: 1, servers: {[id('server:8090')]: ['bad']}})]) {
  test('corrupt guest map fails closed without overwriting it: ' + raw, () => {
    const p = page({items: {[key]: raw}});
    assert.equal(p.list().fail.length, 1);
    assert.equal(p.calls.length, 0);
    p.Lampa.Torserver.remove(X);
    p.Lampa.Torserver.add({});
    p.calls[0].success({hash: X});
    assert.equal(p.items[key], raw);
    assert.equal(p.requests.length, 0);
    assert.ok(p.notices.length >= 3);
  });
}

test('empty guest storage hides existing torrents; clearing storage loses membership only', () => {
  const p = page();
  const before = p.list();
  p.calls[0].success([{hash: X}]);
  assert.equal(before.fail.length, 1);
  p.Lampa.Torserver.add({});
  p.calls[1].success({hash: X});
  delete p.items[key];
  const after = p.list();
  p.calls[2].success([{hash: X}]);
  assert.equal(after.fail.length, 1);
  assert.equal(p.calls.some(call => call.name === 'remove'), false);
});

test('unavailable guest storage fails closed before the original list starts', () => {
  const p = page({faults: {read: true}});
  assert.equal(p.list().fail.length, 1);
  assert.equal(p.calls.length, 0);
  assert.equal(p.requests.length, 0);
  assert.equal(p.notices.length, 1);
});

test('an explicit guest save never reports success if persistence fails', () => {
  const p = page({faults: {write: true}});
  let success = 0, fail = 0;
  p.Lampa.Torserver.add({}, () => success++, () => fail++);
  p.calls[0].success({hash: X});
  assert.equal(success, 0);
  assert.equal(fail, 1);
  assert.equal(p.items[key], undefined);
  assert.equal(p.notices.length, 1);
});

test('TorrServer errors/absence never prune memberships; re-add restores retained hashes only', () => {
  const p = page();
  p.seed([X, Y]);
  const failed = p.list();
  p.calls[0].fail('network', 503);
  assert.deepEqual(failed.fail, [['network', 503]]);
  const empty = p.list();
  p.calls[1].success([]);
  assert.equal(empty.fail.length, 1);
  assert.deepEqual(p.hashes(), [X, Y]);
  p.Lampa.Torserver.remove(X);
  const restored = p.list();
  p.calls[2].success([{hash: X}, {hash: Y}]);
  assert.deepEqual(restored.success, [[[{hash: Y}]]]);
});

test('explicit account add records canonical response and waits to report success', () => {
  const p = page({state: account()});
  const object = {link: 'magnet:whatever', hash: Y};
  const results = [];
  p.Lampa.Torserver.add(object, function (...args) { results.push({args, receiver: this}); });
  assert.equal(p.calls[0].args[0], object);
  const response = {hash: ' ' + X.toUpperCase() + ' '};
  p.calls[0].success(response, 'extra');
  assert.equal(results.length, 0);
  assert.equal(p.requests[0].config.type, 'PUT');
  assert.ok(p.requests[0].config.url.endsWith('/' + X));
  assert.equal(p.requests[0].config.headers['X-Lampa-Csrf'], '1');
  p.requests[0].success('');
  assert.deepEqual(results[0].args, [response, 'extra']);
  assert.equal(results[0].receiver, p.calls[0]);
});

for (const response of [{}, {hash: 'not-a-hash'}, {hash: 12}, null]) test('missing canonical hash cannot claim explicit save success: ' + JSON.stringify(response), () => {
  const p = page({state: account()});
  let success = 0, fail = 0;
  p.Lampa.Torserver.add({hash: X}, () => success++, () => fail++);
  p.calls[0].success(response);
  assert.equal(success, 0);
  assert.equal(fail, 1);
  assert.equal(p.requests.length, 0);
  assert.equal(p.notices.length, 1);
});

for (const status of [0, 401, 409, 503]) test('failed explicit account persistence reports failure, never writes guest storage: ' + status, () => {
  const p = page({state: account()});
  p.seed([Y]);
  let success = 0, fail = 0;
  p.Lampa.Torserver.add({}, () => success++, () => fail++);
  p.calls[0].success({hash: X});
  p.requests[0].fail(status);
  assert.equal(success, 0);
  assert.equal(fail, 1);
  assert.deepEqual(p.hashes(), [Y]);
  assert.equal(p.window.svtlv_account_state.status, status === 401 || status === 409 ? 'checking' : 'signed_in');
});

for (const state of [guest, account()]) for (const saved of [false, true, 'false', 'true']) {
  test('playback remains immediate; membership requires savedb: ' + state.status + '/' + saved, () => {
    const p = page({state, settings: {torrserver_savedb: saved}});
    const results = [];
    p.Lampa.Torserver.hash({}, (...args) => results.push(args));
    const response = {hash: Z};
    p.calls[0].success(response, 'second');
    assert.deepEqual(results, [[response, 'second']]);
    const record = saved === true || saved === 'true';
    if (state.status === 'signed_in') {
      assert.equal(p.requests.length, Number(record));
      if (record) p.requests[0].fail();
    } else {
      assert.deepEqual(p.items[key] ? p.hashes() : [], record ? [Z] : []);
    }
    assert.equal(results.length, 1);
  });
}

test('playback continues with a missing canonical hash or broken guest persistence', () => {
  for (const options of [{}, {faults: {write: true}}]) {
    const p = page({...options, settings: {torrserver_savedb: true}});
    let success = 0;
    p.Lampa.Torserver.hash({}, () => success++);
    p.calls[0].success(options.faults ? {hash: X} : {});
    assert.equal(success, 1);
    assert.equal(p.notices.length, 1);
  }
});

test('failed TorrServer add preserves failure arguments and creates no membership', () => {
  const p = page({state: account()});
  let received, receiver;
  p.Lampa.Torserver.add({}, undefined, function (...args) { received = args; receiver = this; });
  p.calls[0].fail('timeout', {code: 1});
  assert.deepEqual(received, ['timeout', {code: 1}]);
  assert.equal(receiver, p.calls[0]);
  assert.equal(p.requests.length, 0);
});

test('guest adds merge freshly read storage across tabs and servers; hashes are idempotent', () => {
  const items = {};
  const a = page({items}), b = page({items});
  a.Lampa.Torserver.add({}); b.Lampa.Torserver.add({});
  a.calls[0].success({hash: X}); b.calls[0].success({hash: Y});
  a.Lampa.Torserver.add({}); a.calls[1].success({hash: X});
  assert.deepEqual(a.hashes(), [X, Y]);
  b.changeURL('http://other');
  b.Lampa.Torserver.add({}); b.calls[1].success({hash: Z});
  a.Lampa.Torserver.remove(X);
  assert.deepEqual(a.hashes(), [Y]);
  assert.deepEqual(b.hashes(), [Z]);
});

test('guest storage changes refresh only a current guest My Torrents screen', () => {
  const p = page({activity: {component: 'mytorrents'}});
  p.flush();
  p.storageEvent(); p.flush();
  assert.equal(p.replacements.length, 2);
  p.storageEvent('unrelated'); p.flush();
  assert.equal(p.replacements.length, 2);
  p.storageEvent(null); p.flush();
  assert.equal(p.replacements.length, 3);
  p.setState(account()); p.flush();
  p.storageEvent(); p.flush();
  assert.equal(p.replacements.length, 4);
});

test('account changes before add response or persistence completion suppress stale writes/callbacks', () => {
  for (const beforeWrite of [true, false]) {
    const p = page({state: account()});
    let success = 0;
    p.Lampa.Torserver.add({}, () => success++);
    if (!beforeWrite) p.calls[0].success({hash: X});
    p.setState({...guest, generation: 2});
    if (beforeWrite) p.calls[0].success({hash: X});
    else p.requests[0].success('');
    assert.equal(success, 0);
    assert.equal(p.items[key], undefined);
    assert.equal(p.requests.length, beforeWrite ? 0 : 1);
  }
  const p = page();
  p.Lampa.Torserver.add({});
  p.setState(account('Bob', 2));
  p.calls[0].success({hash: X});
  assert.equal(p.items[key], undefined);
  assert.equal(p.requests.length, 0);
});

test('login/logout retain guest entries without copying account memberships', () => {
  const p = page();
  p.seed([X]);
  p.setState(account('Alice', 2));
  p.Lampa.Torserver.add({}); p.calls[0].success({hash: Y}); p.requests[0].success('');
  p.setState({...guest, generation: 3});
  const result = p.list(); p.calls[1].success([{hash: X}, {hash: Y}]);
  assert.deepEqual(result.success, [[[{hash: X}]]]);
});

test('account remove issues only DELETE; missing membership is successful', () => {
  const p = page({state: account()});
  let success = 0;
  p.Lampa.Torserver.remove(' ' + X.toUpperCase() + ' ', () => success++);
  assert.equal(p.calls.length, 0);
  assert.equal(p.requests[0].config.type, 'DELETE');
  assert.equal(p.requests[0].config.headers['X-Lampa-Csrf'], '1');
  assert.ok(p.requests[0].config.url.endsWith('/' + X));
  p.requests[0].success('');
  assert.equal(success, 1);
  p.Lampa.Torserver.remove(X);
  p.requests[1].success('');
  assert.equal(p.calls.length, 0, 'no-callback long-press never invokes original removal');
});

for (const faults of [{read: true}, {write: true}, {silentWrite: true}]) test('guest removal failure reconciles after optimistic disable: ' + JSON.stringify(faults), () => {
  const p = page({activity: {component: 'mytorrents'}, faults});
  p.seed([X]); p.flush();
  let success = 0, fail = 0;
  p.Lampa.Torserver.remove(X, () => success++, () => fail++);
  assert.equal(success, 0); assert.equal(fail, 1);
  assert.equal(p.calls.length, 0);
  assert.equal(p.replacements.length, 1, 'refresh is deferred past disable()');
  p.flush();
  assert.equal(p.replacements.length, 2);
  assert.ok(p.notices.length);
});

test('account removal timeout reloads only the originating activity, never navigates back', () => {
  for (const left of [false, true]) {
    const p = page({state: account(), activity: {component: 'mytorrents'}});
    p.flush();
    p.Lampa.Torserver.remove(X);
    if (left) p.navigate({component: 'mytorrents', different: true});
    p.requests[0].fail(0);
    p.flush();
    assert.equal(p.replacements.length, left ? 1 : 2);
    assert.equal(p.calls.length, 0);
  }
});

test('removal 401 rechecks identity; stale removal cannot affect the new screen', () => {
  const p = page({state: account()});
  let fail = 0;
  p.Lampa.Torserver.remove(X, undefined, () => fail++);
  p.requests[0].fail(401);
  assert.equal(fail, 1);
  assert.equal(p.window.svtlv_account_state.status, 'checking');
  p.setState(account('Bob', 3));
  p.Lampa.Torserver.remove(Y, undefined, () => fail++);
  p.setState({...guest, generation: 4});
  p.requests[1].fail(401);
  assert.equal(fail, 1);
  assert.equal(p.window.svtlv_account_state.status, 'guest');
  assert.equal(p.items[key], undefined);
});

for (const state of [guest, account()]) for (const selection of ['one', 'two']) {
  test('empty URLs retain upstream add/playback but never expose list or physically remove: ' + state.status + '/' + selection, () => {
    const p = page({state, settings: {torrserver_url: '', torrserver_url_two: '', torrserver_use_link: selection}});
    const result = p.list();
    p.calls[0].success([{hash: X}]);
    assert.equal(result.success.length, 0); assert.equal(result.fail.length, 1);
    p.Lampa.Torserver.add({}); p.calls[1].success({hash: X});
    p.Lampa.Torserver.hash({}); p.calls[2].success({hash: X});
    p.Lampa.Torserver.remove(X);
    assert.equal(p.requests.length, 0);
    assert.equal(p.items[key], undefined);
    assert.equal(p.calls.some(call => call.name === 'remove'), false);
    assert.ok(p.notices.at(-1).includes('Configure'));
  });
}

test('drop remains original for every identity', () => {
  for (const state of [guest, account(), null]) {
    const p = page({state});
    p.Lampa.Torserver.drop(X);
    assert.equal(p.calls[0].name, 'drop');
  }
});

test('invalid removal and missing callbacks cannot reach destructive upstream methods', () => {
  for (const state of [guest, account()]) {
    const p = page({state});
    assert.doesNotThrow(() => p.Lampa.Torserver.remove('bad'));
    p.flush();
    assert.equal(p.calls.length, 0);
    assert.equal(p.requests.length, 0);
  }
});

test('synchronous API exceptions fail closed and unexpected mutation status never reports success', () => {
  const p = page({state: account(), ajaxThrows: true});
  const result = p.list();
  assert.equal(result.fail.length, 1);
  assert.equal(result.success.length, 0);
  assert.doesNotThrow(() => p.Lampa.Torserver.remove(X));
  const q = page({state: account()});
  let success = 0;
  q.Lampa.Torserver.remove(X, () => success++);
  q.requests[0].success('wrong', 200);
  assert.equal(success, 0);
  assert.equal(q.notices.length, 1);
});

test('loader orders ownership then sync, tolerates missing/throwing add-ons, and starts once', () => {
  const scripts = [...html.matchAll(/<script(?:\s[^>]*)?>([\s\S]*?)<\/script>/g)].map(match => match[1]).filter(Boolean);
  for (const missingOwnership of [false, true]) for (const missingSync of [false, true])
    for (const remote of [false, true]) for (const hook of ['absent', 'throw', 'delayed']) {
    const appended = [];
    const window = {location: {href: remote ? 'file:///index.html' : 'https://lampa/'}};
    let bootCalls = 0, resume;
    if (hook !== 'absent') window.svtlv_sync_boot = done => {
      bootCalls++;
      if (hook === 'throw') throw new Error('broken add-on');
      resume = done;
    };
    const context = vm.createContext({window, location: window.location, console: {log() {}},
      AndroidJS: remote ? {getLampaURL: () => 'https://remote/'} : undefined,
      $: () => ({append() {}, removeClass() { assert.fail('network overlay'); }, text() {}}),
      document: {createElement: () => ({}), getElementsByTagName: () => [{appendChild: script => appended.push(script)}]}});
    for (const script of scripts) vm.runInContext(script, context);
    const countMain = () => appended.filter(script => script.src.includes('app.min.js')).length;
    const countSync = () => appended.filter(script => script.src.includes('account-sync.js')).length;
    function missing(script) { script.onerror(); appended.at(-1).onerror(); appended.at(-1).onerror(); }
    assert.equal(countMain(), 0); assert.equal(countSync(), 0);
    const ownership = appended.find(script => script.src.includes('torrserver-ownership.js'));
    ownership.readyState = 'loading'; ownership.onreadystatechange();
    assert.equal(countMain(), 0); assert.equal(countSync(), 0);
    ownership.readyState = 'loaded';
    if (missingOwnership) missing(ownership);
    else { ownership.onload(); ownership.onreadystatechange(); }
    assert.equal(countMain(), 0); assert.equal(countSync(), 1, 'duplicate ownership callbacks load sync only once');
    const sync = appended.find(script => script.src.includes('account-sync.js'));
    sync.readyState = 'loading'; sync.onreadystatechange();
    assert.equal(countMain(), 0);
    sync.readyState = 'loaded';
    if (missingSync) missing(sync);
    else {
      sync.onload();
      if (hook === 'delayed') {
        assert.equal(countMain(), 0, 'bundle waits for the sync hook');
        resume(); resume();
      }
      sync.onreadystatechange();
      if (hook === 'delayed') resume();
    }
    assert.equal(countMain(), 1);
    assert.equal(bootCalls > 0, !missingSync && hook !== 'absent');
    const main = appended.find(script => script.src.includes('app.min.js'));
    assert.equal(main.src, remote ? 'https://remote/app.min.js?v=' + context.cache_version : 'app.min.js?v=' + context.cache_version);
    if (remote) {
      missing(main);
      const fallback = appended.at(-1);
      assert.ok(fallback.src.includes('lampa-main/app.min.js'));
      missing(fallback);
      assert.ok(appended.at(-1).src.startsWith('app.min.js'));
    }
  }
});

test('actual bundle publishes the public API before boot and calls wrapped methods via the object', () => {
  const boot = bundle.slice(bundle.lastIndexOf('if (!window.fitst_load)'));
  assert.ok(boot.indexOf('initClass();') < boot.indexOf('loadApp();'));
  assert.ok(bundle.includes('window.Lampa = {'));
  assert.ok(bundle.includes('Torserver.my(function (result)'));
  assert.ok(bundle.includes('Torserver.add({'));
  assert.ok(bundle.includes('Torserver.hash({'));
  assert.ok(bundle.includes('Torserver.remove(data.hash);'));
});
