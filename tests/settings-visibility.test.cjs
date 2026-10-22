// Exercise the real bundled settings registration and both content-filter paths.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const bundle = fs.readFileSync(path.join(__dirname, '../app.min.js'), 'utf8');
const serviceEnd = bundle.indexOf('  var ServiceLGBT = {');
assert.ok(serviceEnd > 0);
const serviceStart = bundle.lastIndexOf('  function ', serviceEnd);
const service = bundle.slice(serviceStart, serviceEnd);
const serviceName = service.match(/function ([\w$]+)\(/)[1];
const card = bundle.match(/  var Lgbt = (\{[\s\S]*?\n  \});/)[1];
const fullStart = bundle.indexOf('          var key_tags = data.movie.keywords');
const fullEnd = bundle.indexOf('          object.card = data.movie;', fullStart);
assert.ok(fullStart > 0 && fullEnd > fullStart);
const full = '(function (data) { ' + bundle.slice(fullStart, fullEnd) + '\nreturn null; })';
const trigger = bundle.match(/  function trigger\(name, value_default\) \{[\s\S]*?\n  \}/)[0];
const defaultRegistration = bundle.match(/  trigger\('lgbt_content_block', false\);/)[0];

function device({platform = 'webos', region = 'ru', preference, disabled = false, cached = true} = {}) {
  const params = [], requests = [], writes = [];
  const defaults = {}, values = {}, items = {};
  if (preference !== undefined) items.lgbt_content_block = preference;
  const flags = {disable_features: {lgbt: disabled}, lgbt: {'42_movie': true, '43_tv': true}};
  let geoReads = 0;
  const context = vm.createContext({
    defaults, values,
    window: {lampa_settings: flags},
    Storage: {field: name => items[name] ?? defaults[name]},
    Platform: {is: name => name === platform, get: () => platform},
    vpn: {is: names => { geoReads++; return names.includes(region); }},
    Cache: {
      getData: () => Promise.resolve(cached ? flags.lgbt : null),
      rewriteData: (kind, key, value) => { writes.push({kind, key, value}); return Promise.resolve(); }
    },
    Arrays: {isObject: value => value !== null && typeof value === 'object' && !Array.isArray(value)},
    Lampa: {
      SettingsApi: {addParam: param => { params.push(param); defaults[param.param.name] = param.param.default; }},
      Lang: {translate: key => key},
      Network: {silent: (url, success) => requests.push({url, success})}
    },
    Utils$1: {protocol: () => 'https://'},
    object$2: {cub_domain: 'cub.best'},
    Keys: {lgbt: ['gay theme']},
    _this: {emit: (type, data) => ({type, ...data})},
    Color: {circlePattern: () => 'pattern'},
    Template: {elem: () => ({style: {}})}
  });
  vm.runInContext(trigger + '\n' + defaultRegistration, context);
  vm.runInContext(service, context);
  const visible = vm.runInContext('(' + card + ').onVisible', context);
  const applyFull = vm.runInContext(full, context);
  return {params, requests, writes, flags, defaults, items,
    init: () => context[serviceName](), applyFull,
    geoReads: () => geoReads,
    card(id = 42, tv = false) {
      const overlays = [];
      const html = {find(selector) {
        // Element.find is querySelector: no overlay yet means null, not an empty set.
        if (selector === '.card__filter') return overlays.length ? {remove: () => { overlays.length = 0; }} : null;
        assert.equal(selector, '.card__img');
        return {after: layer => overlays.push(layer)};
      }};
      return {overlays, visible: () => visible.call({html, data: {id, first_air_date: tv ? '2020-01-01' : ''}})};
    }
  };
}

for (const platform of ['webos', 'netcast', 'android', 'tizen', 'apple_tv', 'browser']) {
  for (const region of ['ru', 'by', 'il', 'us']) {
    test('portable content setting is visible and effective: ' + platform + '/' + region, async () => {
      const d = device({platform, region});
      assert.equal(d.defaults.lgbt_content_block, false, 'default exists before service initialization');
      d.init();
      await Promise.resolve();
      assert.equal(d.params.length, 1);
      const row = d.params[0];
      assert.equal(row.component, 'more');
      assert.equal(row.param.name, 'lgbt_content_block');
      assert.equal(row.param.type, 'trigger', 'use the standard remote-accessible toggle');
      assert.equal(row.param.default, false);
      assert.equal(d.applyFull({movie: {id: 42}}), null);
      const card = d.card();
      card.visible();
      assert.equal(card.overlays.length, 0);
      d.items.lgbt_content_block = true;
      assert.equal(d.applyFull({movie: {id: 42}}).blocked, true);
      card.visible();
      card.visible();
      assert.equal(card.overlays.length, 1, 'repeated visibility does not duplicate the mask');
      d.items.lgbt_content_block = false;
      card.visible();
      assert.equal(card.overlays.length, 0, 'turning off clears an old mask when the card reappears');
      assert.equal(d.geoReads(), 0, 'neither visibility nor filtering depends on detected region');
    });
  }
}

test('filtering covers both keyword formats, movie lists and TV lists', () => {
  const d = device({preference: true});
  for (const movie of [
    {id: 42},
    {id: 43, first_air_date: '2020-01-01'},
    {id: 99, keywords: {results: [{name: 'Gay Theme'}]}},
    {id: 99, keywords: {keywords: [{name: 'Gay Theme'}]}}
  ]) assert.equal(d.applyFull({movie}).blocked, true);
  assert.equal(d.applyFull({movie: {id: 99}}), null);
  const tv = d.card(43, true);
  tv.visible();
  assert.equal(tv.overlays.length, 1);
});

test('turning filtering off clears cached LGBT marks without removing other content blocks', () => {
  const d = device({preference: true});
  const data = {movie: {id: 42}};
  assert.equal(d.applyFull(data).blocked, true);
  d.items.lgbt_content_block = false;
  assert.equal(d.applyFull(data), null);
  assert.equal(Object.hasOwn(data.movie, 'lgbt'), false);
  data.movie.blocked = true;
  assert.equal(d.applyFull(data).blocked, true, 'unrelated blocks remain enforced');
});

test('feature-disabled builds skip service initialization while filtering follows the saved preference', async () => {
  const d = device({preference: true, disabled: true});
  d.init();
  await Promise.resolve();
  assert.equal(d.params.length, 0);
  assert.equal(d.requests.length, 0);
  const card = d.card();
  card.visible();
  assert.equal(card.overlays.length, 1);
  assert.equal(d.applyFull({movie: {id: 42}}).blocked, true);
  d.items.lgbt_content_block = false;
  card.visible();
  assert.equal(card.overlays.length, 0);
  assert.equal(d.applyFull({movie: {id: 42, lgbt: 'list'}}), null);
});

test('a delayed filter-list response respects the current preference', async () => {
  const d = device({cached: false, preference: false});
  d.init();
  await Promise.resolve();
  assert.equal(d.requests.length, 1);
  d.requests[0].success([{id: 42, type: 'movie'}]);
  assert.equal(d.writes.length, 1);
  assert.equal(d.applyFull({movie: {id: 42}}), null);
  d.items.lgbt_content_block = true;
  assert.equal(d.applyFull({movie: {id: 42}}).blocked, true);
});
