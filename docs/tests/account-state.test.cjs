// Run with: node --test docs/tests/account-state.test.cjs
// Runs the actual add-on, with controllable HTTP callbacks, timers and public Lampa APIs.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../../svtlv/account.js'), 'utf8');
const ownershipSource = fs.readFileSync(path.join(__dirname, '../../svtlv/torrserver-ownership.js'), 'utf8');
const bundle = fs.readFileSync(path.join(__dirname, '../../app.min.js'), 'utf8');
const flag = 'svtlv_account_signed_in';
const signedIn = id => ({authenticated: true, user: {id, name: id, email: id + '@example.test'}});
const guest = {authenticated: false};

function page(options = {}) {
  const requests = [];
  const playback = [];
  const intervals = [];
  const timeouts = [];
  const events = [];
  const listeners = {};
  const items = {...options.storage};
  const settings = {language: 'en', torrserver_url: 'https://server:8090', torrserver_url_two: '',
    torrserver_use_link: 'one', torrserver_savedb: true, ...options.settings};
  const notices = [];
  const selections = [];
  const components = [];
  let headIcon;
  const storage = {
    getItem(key) {
      if (options.storageError) throw new Error('storage unavailable');
      return items[key] ?? null;
    },
    setItem(key, value) {
      if (options.storageError) throw new Error('storage unavailable');
      items[key] = value;
    },
    removeItem(key) {
      if (options.storageError) throw new Error('storage unavailable');
      delete items[key];
    }
  };
  class Element {
    constructor() { this.length = 1; this.children = []; this.handlers = {}; this.value = ''; }
    find() { return new Element(); }
    empty() { this.children = []; return this; }
    append(child) { this.children.push(child); return this; }
    text(value) { if (value === undefined) return this.value; this.value = value; return this; }
    html() { return this.value; }
    on(name, fn) { this.handlers[name] = fn; return this; }
    addClass() { return this; }
    hasClass() { return false; }
    before() { return this; }
    remove() { return this; }
    replaceWith() { return this; }
  }
  function $(value) {
    const element = new Element();
    if (typeof value === 'string' && value.includes('open--account')) headIcon = element;
    return element;
  }
  $.trim = text => text.trim();
  $.contains = () => true;
  $.ajax = settings => {
    const request = {
      settings,
      respond(data, status = 200) {
        if (settings.success) settings.success(data);
        if (settings.complete) settings.complete({status, responseText: JSON.stringify(data)});
      },
      fail() {
        if (settings.error) settings.error();
        if (settings.complete) settings.complete({status: 503});
      },
      abort() {}
    };
    requests.push(request);
    return request;
  };
  const listener = {
    follow(name, fn) { (listeners[name] ||= []).push(fn); },
    send(name, event) {
      if (name === 'svtlv_account' && event.type === 'state') {
        events.push({state: {...event.state}, avatar: headIcon?.children[0]?.value, flag: items[flag],
          leaving: window.svtlv_account_leaving, href: window.location.href});
      }
      for (const fn of listeners[name] || []) fn(event);
    }
  };
  const Lampa = {
    Listener: listener,
    Storage: {get: name => settings[name], field: name => settings[name], listener},
    Torserver: {hash(object, success, fail) { playback.push({object, success, fail}); }},
    Activity: {active: () => null, all: () => []},
    SettingsApi: {addComponent: component => components.push(component)},
    Settings: {listener, update() {}},
    Template: {add() {}, string: () => '<svg></svg>'},
    Head: {render: () => new Element()},
    Account: {Permit: {access: false}},
    Platform: {tv: () => !!options.tv, mouse: () => false},
    Select: {show: selection => selections.push(selection), listener},
    Noty: {show: notice => notices.push(notice)},
    Controller: {toggle() {}, enabled: () => ({name: 'head'})},
    Utils: {qrcode() {}},
    Modal: {open() {}, close() {}}
  };
  const ip = bundle.match(/  function ip\(\) \{[\s\S]*?\n  \}/)[0];
  Lampa.Torserver.ip = vm.runInNewContext(ip + '\nip;', {Storage: Lampa.Storage});
  const window = {
    location: {protocol: options.protocol || 'https:', hash: '', pathname: '/', search: ''},
    localStorage: storage,
    lampa_settings: {account_use: false},
    appready: options.ready !== false
  };
  if (!options.lateLampa) window.Lampa = Lampa;
  const context = vm.createContext({window, Lampa, $, console: {log() {}},
    document: {documentElement: {}, createElement: () => ({})},
    setTimeout: fn => { timeouts.push(fn); return timeouts.length; },
    setInterval: fn => { intervals.push(fn); return intervals.length; },
    clearTimeout() {}, clearInterval() {}});
  vm.runInContext(source, context);
  if (options.ownership) vm.runInContext(ownershipSource, context);
  return {
    window, requests, events, items, components, notices, selections, intervals, timeouts, playback,
    play(success, fail) { Lampa.Torserver.hash({link: 'magnet:test'}, success, fail); },
    state: () => ({...window.svtlv_account_state}),
    ready() { window.appready = true; listener.send('app', {type: 'ready'}); },
    installLampa() { window.Lampa = Lampa; timeouts.shift()(); },
    expire() { listener.send('svtlv_account', {type: 'expired'}); },
    heartbeat() { intervals[0](); },
    reloadScript() { vm.runInContext(source, context); },
    logout() {
      headIcon.handlers['hover:enter']();
      const menu = selections.at(-1);
      menu.items.find(item => item.title === 'Log out').onSelect();
      const confirmation = selections.at(-1);
      confirmation.onSelect(confirmation.items[0]);
    },
    loginTV() {
      headIcon.handlers['hover:enter']();
      selections.at(-1).items[0].onSelect();
      requests.at(-1).respond({user_code: 'ABCD', verification_uri: 'https://sso.example.test',
        verification_uri_complete: 'https://sso.example.test/code', expires_in: 600, interval: 5});
      timeouts.at(-1)();
      return requests.at(-1);
    }
  };
}

test('publishes page-local checking before Lampa and app readiness; waits to request the session', () => {
  const p = page({ready: false, lateLampa: true, storage: {[flag]: '1'}});
  assert.deepEqual(p.state(), {status: 'checking', generation: 0, user_id: ''});
  assert.equal(p.requests.length, 0);
  p.installLampa();
  p.expire();
  assert.equal(p.state().status, 'checking');
  assert.equal(p.requests.length, 0);
  p.ready();
  assert.equal(p.requests.length, 1);
  assert.equal(p.requests[0].settings.url, '/api/v1/session');
  assert.equal(p.events.at(-1).state.status, 'checking');
  p.requests[0].respond(guest);
  assert.equal(p.state().status, 'guest');
  assert.equal(p.state().user_id, '');
  assert.equal(p.items[flag], undefined);
  assert.equal(p.components.length, 1);
  p.ready();
  p.reloadScript();
  assert.equal(p.requests.length, 1, 'readiness and load guards avoid duplicate installation');
});

for (const storage of [{}, {[flag]: '1'}]) {
  test('initial outage fails closed regardless of the persisted flag ' + JSON.stringify(storage), () => {
    const p = page({storage});
    p.requests[0].fail();
    assert.equal(p.state().status, 'unavailable');
    assert.equal(p.state().user_id, '');
    assert.equal(p.components.length, 0);
    p.heartbeat();
    assert.equal(p.state().status, 'checking');
    p.requests[1].respond(guest);
    assert.equal(p.state().status, 'guest');
    assert.equal(p.components.length, 1, 'successful retry activates the account UI');
  });
}

test('failed signed-in session refresh keeps direct playback available without recording membership', () => {
  const p = page({ownership: true});
  p.requests[0].respond(signedIn('Alice'));
  p.heartbeat();
  assert.equal(p.state().status, 'checking');
  let duringRefresh = 0, duringOutage = 0;
  p.play(() => duringRefresh++);
  assert.equal(p.playback.length, 1);
  p.requests[1].fail();
  assert.equal(p.state().status, 'unavailable');
  p.play(() => duringOutage++);
  assert.equal(p.playback.length, 2);
  p.playback[0].success({hash: 'a'.repeat(40)});
  p.playback[1].success({hash: 'b'.repeat(40)});
  assert.equal(duringRefresh, 1);
  assert.equal(duringOutage, 1);
  assert.equal(p.requests.length, 2, 'only session requests; no ownership writes');
  assert.equal(p.items.svtlv_torrserver_guest_hashes, undefined);
  assert.equal(p.notices.length, 0);
});

for (const settings of [
  {torrserver_use_link: 'two', torrserver_url_two: 'http://secondary:8090'},
  {torrserver_use_link: 'one', torrserver_url: '', torrserver_url_two: 'http://secondary:8090'}
]) {
  test('confirmed account playback records membership for the effective secondary endpoint: ' + settings.torrserver_use_link, () => {
    const p = page({ownership: true, settings});
    p.requests[0].respond(signedIn('Alice'));
    let played = 0;
    p.play(() => played++);
    p.playback[0].success({hash: 'a'.repeat(40)});
    const server = crypto.createHash('sha256').update('secondary:8090').digest('hex');
    assert.equal(p.requests[1].settings.url, '/api/v1/torrservers/' + server + '/hashes/' + 'a'.repeat(40));
    assert.equal(p.requests[1].settings.headers['X-Lampa-Expected-User'], 'Alice');
    assert.equal(played, 1);
  });
}

test('authenticated response publishes identity and flag; expiry immediately blocks it', () => {
  const p = page();
  p.requests[0].respond(signedIn('Alice'));
  assert.equal(p.state().status, 'signed_in');
  assert.equal(p.state().user_id, 'Alice');
  assert.equal(p.items[flag], '1');
  const previous = p.state().generation;
  p.expire();
  assert.equal(p.state().status, 'checking');
  assert.ok(p.state().generation > previous);
  assert.equal(p.state().user_id, '');
  p.requests[1].respond(guest);
  assert.equal(p.state().status, 'guest');
  assert.equal(p.items[flag], undefined);
  assert.equal(p.events.at(-1).avatar, '', 'avatar has cleared before guest listeners run');
  p.heartbeat();
  p.requests[2].respond(signedIn('Bob'));
  assert.equal(p.state().user_id, 'Bob');
  assert.equal(p.events.at(-1).avatar, 'B', 'avatar has updated before account listeners run');
  assert.equal(p.events.at(-1).flag, '1');
  assert.equal(p.components.length, 1);
});

test('only a currently confirmed guest survives an outage, including an expiry recheck', () => {
  const p = page();
  p.requests[0].respond(guest);
  p.expire();
  p.requests[1].fail();
  assert.equal(p.state().status, 'guest');
  p.heartbeat();
  p.requests[2].respond(signedIn('Alice'));
  p.heartbeat();
  p.requests[3].fail();
  assert.equal(p.state().status, 'unavailable', 'a previously confirmed guest does not survive sign-in');
  assert.equal(p.state().user_id, '');
  assert.equal(p.items[flag], undefined);
  assert.equal(p.events.at(-1).avatar, '');
});

for (const staleResult of [signedIn('Alice'), guest, null]) {
  test('drops a pre-expiry session response ' + JSON.stringify(staleResult), () => {
    const p = page();
    p.expire();
    const generation = p.state().generation;
    p.requests[0].respond(staleResult);
    assert.equal(p.state().generation, generation);
    assert.equal(p.state().status, 'checking');
    p.requests[1].respond(signedIn('Bob'));
    assert.equal(p.state().user_id, 'Bob');
  });
}

test('ignores older concurrent refresh successes and failures after the latest identity resolves', () => {
  const p = page();
  p.requests[0].respond(guest);
  p.heartbeat();
  p.heartbeat();
  p.expire();
  p.requests[3].respond(signedIn('Bob'));
  const expected = p.state();
  p.requests[1].respond(signedIn('Alice'));
  p.requests[2].fail();
  assert.deepEqual(p.state(), expected);
});

for (const data of [null, {}, {authenticated: 'yes'}, {authenticated: true}, {authenticated: true, user: {}}]) {
  test('malformed initial session fails closed ' + JSON.stringify(data), () => {
    const p = page();
    p.requests[0].respond(data);
    assert.equal(p.state().status, 'unavailable');
    assert.equal(p.components.length, 0);
    assert.equal(p.items[flag], undefined);
  });
}

test('unavailable localStorage does not prevent confirmed sign-in or guest transitions', () => {
  const p = page({storageError: true});
  p.requests[0].respond(signedIn('Alice'));
  assert.equal(p.state().status, 'signed_in');
  p.expire();
  p.requests[1].respond(guest);
  assert.equal(p.state().status, 'guest');
});

test('successful logout pauses heartbeat and publishes guest before redirect', () => {
  const p = page();
  p.requests[0].respond(signedIn('Alice'));
  p.logout();
  assert.equal(p.requests[1].settings.url, '/api/v1/auth/logout?sso=1');
  assert.equal(p.requests[1].settings.headers['X-Lampa-Csrf'], '1');
  p.heartbeat();
  p.expire();
  assert.equal(p.requests.length, 2, 'no new session renewal during logout');
  p.requests[1].respond({logout_url: 'https://sso.example.test/logout'}, 200);
  assert.equal(p.state().status, 'guest');
  assert.equal(p.items[flag], undefined);
  assert.equal(p.events.at(-1).leaving, true, 'listeners see the leaving flag before guest cleanup');
  assert.equal(p.events.at(-1).href, undefined, 'guest cleanup runs before SSO navigation');
  assert.equal(p.window.location.href, 'https://sso.example.test/logout');
  p.heartbeat();
  p.requests[2].fail();
  assert.equal(p.state().status, 'guest', 'logout confirms guest for later outages');
});

test('logout waits for a renewal already in flight, even if expiry invalidated its result', () => {
  // Device sign-in restores the signed-in UI while invalidating a pending GET.
  const q = page({tv: true});
  q.requests[0].respond(guest);
  const login = q.loginTV();
  q.heartbeat();
  login.respond(signedIn('Alice'));
  q.logout();
  assert.equal(q.requests.length, 4, 'logout waits for the pending GET');
  q.expire();
  q.requests[3].respond(signedIn('Old'));
  assert.equal(q.state().status, 'checking', 'old renewal cannot restore the account');
  assert.equal(q.requests[4].settings.url, '/api/v1/auth/logout');
  q.requests[4].respond({}, 204);
  assert.equal(q.state().status, 'guest');
  assert.equal(q.events.at(-1).leaving, undefined, 'local TV logout allows the cleanup reload');
});

test('logout failure preserves an unchanged account; expiry during failure starts a recheck', () => {
  const p = page();
  p.requests[0].respond(signedIn('Alice'));
  p.logout();
  p.requests[1].fail();
  assert.equal(p.state().user_id, 'Alice');
  assert.equal(p.window.svtlv_account_leaving, undefined);
  p.logout();
  p.expire();
  p.requests[2].fail();
  assert.equal(p.requests[3].settings.url, '/api/v1/session');
  p.requests[3].fail();
  assert.equal(p.state().status, 'unavailable');
});

test('TV device sign-in invalidates a pending guest session response', () => {
  const p = page({tv: true});
  p.requests[0].respond(guest);
  const login = p.loginTV();
  p.heartbeat();
  login.respond(signedIn('Alice'));
  const expected = p.state();
  p.requests[3].respond(guest);
  assert.deepEqual(p.state(), expected);
  assert.equal(p.items[flag], '1');
});

test('a new page cannot reuse a previous guest readiness state', () => {
  const first = page();
  first.requests[0].respond(guest);
  const next = page({storage: first.items});
  assert.equal(next.state().status, 'checking');
  next.requests[0].fail();
  assert.equal(next.state().status, 'unavailable');
});

test('non-HTTP origin does not install state, listeners or requests', () => {
  const p = page({protocol: 'file:'});
  assert.equal(p.window.svtlv_account_state, undefined);
  assert.equal(p.requests.length, 0);
  assert.equal(p.window.svtlv_account_loaded, undefined);
});
