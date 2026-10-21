// Run with: node --test backend/pkg/storage/testdata/torrserver-one-link.test.cjs
// Executes the actual bundle functions without booting Lampa or contacting a server.
// The historical filename is retained for existing CI commands.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');
const bundle = fs.readFileSync(path.join(__dirname, '../../../../app.min.js'), 'utf8');

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
    Base64: {encode: text => Buffer.from(text).toString('base64')},
    values: {}, defaults: {torrserver_auth: false, torrserver_preload: false, torrserver_gts: false}
  });
  vm.runInContext(source('get$1') + source('checkEmptyUrl') + source('select') + source('field$1'), context);
  for (const registration of bundle.matchAll(/  select\('torrserver_[^']+',[\s\S]*?\);/g)) {
    vm.runInContext(registration[0], context);
  }
  context.Storage = {
    get: context.get$1,
    field: context.field$1,
    value: key => localStorage.getItem(key) || ''
  };
  context.Utils$1 = {checkEmptyUrl: context.checkEmptyUrl};
  vm.runInContext(source('ip') + source('url$5'), context);
  function set(key, value) {
    localStorage.setItem(key, value);
    delete context.readed[key];
  }
  context.Storage.set = set;
  return {items, writes, notices, context, options, set};
}

const basicAuth = 'Basic ' + Buffer.from('user:pass').toString('base64');
const endpoints = {torrserver_url: 'http://first', torrserver_url_two: 'http://second'};
const credentials = {torrserver_auth: 'true', torrserver_login: 'user', torrserver_password: 'pass'};

test('original settings controls retain their order, labels and remote selectors', () => {
  const line = bundle.split('\n').find(line => line.startsWith('  var ') && line.includes('data-name=\\"torrserver_url\\"'));
  const html = JSON.parse(line.slice(line.indexOf(' = ') + 3, -1));
  const controls = [...html.matchAll(/<div class="settings-param selector" data-type="(select|input)" data-name="(torrserver_[^"]+)"[^>]*>\s*<div class="settings-param__name">([^<]+)<\/div>/g)];
  assert.deepEqual(controls.slice(0, 3).map(match => match.slice(1)), [
    ['select', 'torrserver_use_link', '#{settings_server_link}'],
    ['input', 'torrserver_url', '#{settings_server_link_one}'],
    ['input', 'torrserver_url_two', '#{settings_server_link_two}']
  ]);
  for (const suffix of ['one', 'two']) {
    assert.ok(html.includes('#{settings_server_link_' + suffix + '_descr}'));
  }
  const d = device();
  assert.equal(d.context.Storage.field('torrserver_use_link'), 'one');
  assert.deepEqual({...d.context.values.torrserver_use_link}, {
    one: '#{settings_param_link_use_one}', two: '#{settings_param_link_use_two}'
  });
  assert.equal(d.context.Storage.field('torrserver_url'), '');
  assert.equal(d.context.Storage.field('torrserver_url_two'), '');
  assert.deepEqual(d.writes, [], 'reading defaults must not persist or migrate values');
});

// Invalid values keep upstream's per-path comparisons: ip/parser/auth test for
// "two", whereas opening status settings tests for "one".
for (const selection of [undefined, '', 'one', 'two', 'invalid']) {
  for (const one of ['', 'http://first']) {
    for (const two of ['', 'http://second']) {
      const values = {torrserver_url: one, torrserver_url_two: two};
      if (selection !== undefined) values.torrserver_use_link = selection;
      const effective = selection === 'two' ? two || one : one || two;
      const selected = selection === 'two' ? two : one;
      const label = JSON.stringify([selection, one, two]);

      test('endpoint selection and fallback: ' + label, () => {
        const d = device(values);
        assert.equal(d.context.ip(), effective);
        assert.equal(d.context.url$5(), effective);
        assert.deepEqual(d.writes, []);
        assert.deepEqual(d.items, values, 'routing must not swap or rewrite either URL');
      });

      test('parser uses selected field without ip fallback: ' + label, () => {
        const d = device({...values, parser_torrent_type: 'torrserver'});
        const urls = [], failures = [];
        d.context.torrserver = (params, url, done) => {urls.push(url); done({Results: []});};
        d.context.Lang = {translate: key => key};
        vm.runInContext(source('get$5'), d.context);
        d.context.get$5({}, () => {}, error => failures.push(error));
        assert.deepEqual(urls, selected ? [selected] : []);
        assert.deepEqual(failures, selected ? [] : ['torrent_parser_set_link: TorrServer']);
      });

      test('authentication uses selected field without ip fallback: ' + label, () => {
        const d = device({...values, ...credentials});
        const headers = [];
        const xhr = {setRequestHeader: (...args) => headers.push(args)};
        vm.runInContext(source('beforeSend', '        '), d.context);
        for (const endpoint of ['http://first', 'http://second', 'http://unrelated']) {
          d.context.params = {url: endpoint + '/torrents'};
          headers.length = 0;
          d.context.beforeSend(xhr);
          assert.deepEqual(headers, selected && endpoint === selected ? [['Authorization', basicAuth]] : []);
        }
      });

      test('torrent requests and stream URLs follow ip fallback: ' + label, () => {
        const d = device(values);
        const calls = [];
        d.context.clear$e = () => {};
        d.context.network$b = {silent: (url, done, fail, body) => {
          calls.push([url, JSON.parse(body)]); done([{hash: 'hash'}]);
        }};
        d.context.gstWork = () => false;
        vm.runInContext(source('my') + source('stream') + source('toPlayUrl'), d.context);
        let response;
        d.context.my(value => {response = value;}, () => assert.fail('unexpected request failure'));
        assert.deepEqual(calls, [[effective + '/torrents', {action: 'list'}]]);
        assert.equal(response[0].hash, 'hash');
        const stream = effective + '/stream/movie%20name.mkv?link=hash&index=2&';
        assert.equal(d.context.stream('folder/movie name.mkv', 'hash', 2), stream + 'play');
        d.set('torrserver_preload', 'true');
        assert.equal(d.context.stream('folder/movie name.mkv', 'hash', 2), stream + 'preload');
        assert.equal(d.context.toPlayUrl(stream + 'preload'), stream + (effective ? 'play' : 'preload'));
      });
    }
  }
}

for (const selection of ['one', 'two']) {
  for (const protocol of ['http:', 'https:']) {
    for (const url of ['host:8090', 'http://host:8090', 'https://host:8090']) {
      test('URL scheme is preserved: ' + selection + ' ' + protocol + ' ' + url, () => {
        const key = selection === 'two' ? 'torrserver_url_two' : 'torrserver_url';
        const d = device({...endpoints, [key]: url, torrserver_use_link: selection}, {protocol});
        assert.equal(d.context.ip(), url);
        assert.equal(d.context.url$5(), url.includes('://') ? url : protocol + '//' + url);
      });
    }
  }
}

test('authentication toggle and explicit request headers retain their behavior', () => {
  const d = device({...endpoints, ...credentials, torrserver_auth: 'false', torrserver_use_link: 'two'});
  const headers = [];
  d.context.params = {url: 'http://second/torrents', beforeSend: {name: 'X-Custom', value: 'value'}};
  vm.runInContext(source('beforeSend', '        '), d.context);
  d.context.beforeSend({setRequestHeader: (...args) => headers.push(args)});
  assert.deepEqual(headers, [['X-Custom', 'value']]);
});

// Exercise the actual UI hooks with a small jQuery/controller stand-in.
function settingsDevice(values, options) {
  const d = device(values, options);
  const settingsHooks = [], changes = [], queries = [], statusCalls = [], speeds = [];
  let enter;
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
  d.context.start$2 = params => speeds.push({...params});
  d.context.torrent_net = {clear: () => {}, timeout: () => {}, native: (url, done, fail, data, head) => statusCalls.push([url, head])};
  vm.runInContext(source('init$h') + source('init$b') + source('check'), d.context);
  d.context.init$h();
  d.context.init$b();
  const open = () => settingsHooks.forEach(hook => hook({name: 'server', body: {}}));
  const change = (name, value) => {
    if (value !== undefined) d.set(name, value);
    changes.forEach(hook => hook({name, value}));
  };
  return {...d, open, change, enter: () => enter(), queries, statusCalls, speeds};
}

for (const selection of [undefined, 'one', 'two', 'invalid']) {
  for (const one of ['', 'http://first']) {
    for (const two of ['', 'http://second']) {
      test('speed fallback and direct status selection: ' + JSON.stringify([selection, one, two]), () => {
        const values = {torrserver_url: one, torrserver_url_two: two, ...credentials};
        if (selection !== undefined) values.torrserver_use_link = selection;
        const d = settingsDevice(values);
        const effective = selection === 'two' ? two || one : one || two;
        const status = selection === undefined || selection === 'one' ? one : two;
        d.open();
        d.enter();
        assert.deepEqual(d.speeds, effective ? [{url: effective + '/download/300', login: 'user', password: 'pass'}] : []);
        assert.deepEqual(d.statusCalls.map(call => call[0]), status ? [status] : []);
        if (status) assert.equal(d.statusCalls[0][1].headers.Authorization, basicAuth);
        assert.ok(d.queries.includes('[data-name="torrserver_url_two"]'), 'speed button follows the second input');
      });
    }
  }
}

test('both URL changes and selector changes check their original status targets', () => {
  const d = settingsDevice({...endpoints, torrserver_use_link: 'one', torrserver_auth: 'false'});
  d.open();
  d.change('torrserver_url_two', 'http://edited-second');
  d.change('torrserver_use_link', 'two');
  d.change('torrserver_url', 'http://edited-first');
  d.change('torrserver_use_link', 'one');
  assert.deepEqual(d.statusCalls.map(call => call[0]), [
    'http://first', 'http://edited-second', 'http://edited-second', 'http://edited-first', 'http://edited-first'
  ]);
  assert.ok(d.statusCalls.every(call => !call[1].headers));
  d.change('torrserver_url', '');
  d.change('torrserver_use_link', 'one');
  assert.equal(d.statusCalls.length, 5, 'status checks never fall back from an empty selected field');
  d.enter();
  assert.deepEqual(d.speeds, [{url: 'http://edited-second/download/300'}]);
});

test('status and speed tests preserve scheme handling for the selected URL', () => {
  const d = settingsDevice({torrserver_url_two: 'second:8090', torrserver_use_link: 'two', torrserver_auth: 'false'}, {protocol: 'https:'});
  d.open(); d.enter();
  assert.equal(d.statusCalls[0][0], 'https://second:8090');
  assert.equal(d.speeds[0].url, 'https://second:8090/download/300');
});

test('search source reacts to both URLs, the selector and parser toggle', () => {
  const d = device({...endpoints, torrserver_use_link: 'two', parse_in_search: 'true'});
  const hooks = [], added = [], removed = [];
  d.context.Storage.listener = {follow: (name, callback) => hooks.push(callback)};
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
  assert.equal(removed.length, 2);
  assert.equal(added.length, 3);
  d.set('torrserver_url_two', '');
  hooks[0]({name: 'torrserver_url_two'});
  assert.equal(added.length, 4, 'search source registration still uses ip fallback');
  d.set('torrserver_url', '');
  hooks[0]({name: 'torrserver_url'});
  assert.equal(removed.length, 4);
  assert.equal(added.length, 4, 'both empty disables the source');
  d.set('torrserver_url', 'http://restored');
  hooks[0]({name: 'torrserver_url'});
  assert.equal(added.length, 5);
  d.set('parse_in_search', 'false');
  hooks[0]({name: 'parse_in_search'});
  assert.equal(removed.length, 6);
  assert.equal(added.length, 5);
  hooks[0]({name: 'unrelated_setting'});
  assert.equal(removed.length, 6);
});
