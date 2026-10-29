// Run the add-on with the real bundled parameter binder and remote-enter events.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {test} = require('node:test');

const source = fs.readFileSync(path.join(__dirname, '../svtlv/settings.js'), 'utf8');
const bundle = fs.readFileSync(path.join(__dirname, '../app.min.js'), 'utf8');
function method(name) {
  const start = bundle.indexOf('  function ' + name + '(');
  assert.ok(start >= 0, name);
  return bundle.slice(start, bundle.indexOf('\n  }', start) + 4);
}

function page(options = {}) {
  const items = {language: 'en', ...options.items};
  const defaults = {}, values = {}, params = [], writes = [], modals = [], selections = [], controllers = [];
  const events = [], timers = [], appListeners = {}, settingListeners = {}, storageListeners = {};
  const listener = handlers => ({
    follow(name, fn) { (handlers[name] ||= []).push(fn); },
    send(name, value) { for (const fn of handlers[name] || []) fn(value); }
  });
  class Element {
    constructor(data = {}) { this.props = data; this.length = 1; this.handlers = {}; this.children = {}; this.hidden = !!data.hidden; }
    on(name, fn) { this.handlers[name] = fn; return this; }
    each(fn) { fn.call(this); return this; }
    eq() { return this; }
    data(name, value) { if (arguments.length === 1) return this.props[name]; this.props[name] = value; return this; }
    attr() { return undefined; }
    find(name) { return this.children[name] ||= new Element(); }
    text(value) { if (arguments.length === 0) return this.value; this.value = value; return this; }
    append() { return this; }
    after() { return this; }
    remove() { this.detached = true; return this; }
    addClass() { this.hidden = true; return this; }
    removeClass() { this.hidden = false; return this; }
    enter() { this.handlers['hover:enter']({target: this}); }
  }
  const Controller = {enabled: () => ({name: 'settings_component'}), toggle: name => controllers.push(name)};
  const Lang = {translate: key => ({settings_param_yes: 'Yes', settings_param_no: 'No',
    '#{settings_param_yes}': 'Yes', '#{settings_param_no}': 'No'})[key] || key};
  const Storage = {
    get(name, fallback) {
      const value = items[name] ?? fallback;
      return value === 'true' ? true : value === 'false' ? false : value;
    },
    field(name) { return this.get(name, defaults[name] + ''); },
    set(name, value, nolisten) {
      items[name] = String(value); writes.push({name, value, nolisten});
      if (!nolisten) this.listener.send('change', {name, value});
    }, listener: listener(storageListeners)
  };
  const Manifest = {
    cub_mirrors: ['cub.best', 'cub.black'],
    get cub_domain() { return this.cub_mirrors.includes(items.cub_domain) ? items.cub_domain : 'cub.best'; }
  };
  const Lampa = {
    Storage, Manifest, Controller, Lang,
    Account: {Permit: {child: !!options.child}},
    Settings: {listener: listener(settingListeners)},
    Listener: listener(appListeners),
    SettingsApi: {
      getParam: component => params.filter(p => p.component === component),
      addParam(data) {
        params.push(data);
        if (data.param.type === 'trigger') context.trigger(data.param.name, data.param.default);
        if (data.param.type === 'select') context.select(data.param.name, data.param.values, data.param.default);
      }
    },
    Params: {listener: {send: name => events.push(name)}},
    Utils: {protocol: () => 'https://'},
    Modal: {open: modal => modals.push(modal), close: () => events.push('close')}
  };
  const window = {location: {protocol: options.protocol || 'https:'}, lampa_settings: {...options.flags},
    Lampa: options.lateLampa ? undefined : Lampa, appready: options.ready !== false};
  const context = vm.createContext({
    window, Lampa, defaults, values, Storage, Controller, Lang,
    $: element => typeof element === 'string' ? new Element() : element,
    Arrays: {isArray: Array.isArray, getKeys: Object.keys},
    Select: {show: selection => selections.push(selection)},
    listener$1: {send() {}}, Navigator: {focused() {}},
    updateInfuseLaunchVisibility() {},
    setTimeout: fn => timers.push(fn), console: {log() {}}
  });
  vm.runInContext(['trigger', 'select', 'bind', 'update$3'].map(method).join('\n'), context);
  Object.assign(Lampa.Params, {select: context.select, update: context.update$3});
  context.trigger('adult_content_view', false);
  vm.runInContext(source, context);
  return {params, items, writes, modals, selections, controllers, events, timers, Lampa, window, context,
    reload: () => vm.runInContext(source, context),
    render(name) {
      const param = params.find(p => p.param.name === name);
      assert.ok(param, name);
      const type = param.param.type === 'trigger' ? 'toggle' : param.param.type;
      const item = new Element({name, type, static: type === 'button', onChange: param.onChange});
      param.onRender(item);
      if (item.length) context.bind(item);
      return item;
    },
    mirror() {
      const item = new Element({name: 'cub_domain', type: 'select', hidden: true});
      context.select('cub_domain', {'cub.best': 'cub.best'}, 'cub.best');
      context.bind(item);
      Lampa.Settings.listener.send('open', {name: 'account', body: {find: () => item}});
      return item;
    }
  };
}

test('startup works before or after app ready and duplicate loads register nothing twice', () => {
  const a = page({ready: false});
  assert.equal(a.params.length, 0);
  a.Lampa.Listener.send('app', {type: 'ready'});
  assert.equal(a.params.length, 8);
  a.reload(); a.Lampa.Listener.send('app', {type: 'ready'});
  assert.equal(a.params.length, 8);
  const b = page({lateLampa: true});
  assert.equal(b.params.length, 0);
  b.window.Lampa = b.Lampa;
  b.timers.shift()();
  assert.equal(b.params.length, 8);
});

test('standard remote controls edit the existing subtitle and segment values', () => {
  const p = page({items: {subtitles_size: 'large', player_segments_ad: 'none'}});
  assert.equal(p.writes.length, 0, 'registration never overwrites existing choices');
  for (const [key, value] of [['subtitles_size', 'small'], ['player_segments_skip', 'user'], ['player_segments_ad', 'auto']]) {
    p.render(key).enter();
    const selection = p.selections.at(-1);
    selection.onSelect(selection.items.find(item => item.value === value));
    assert.equal(p.items[key], value);
  }
  for (const key of ['subtitles_stroke', 'subtitles_backdrop']) {
    const before = p.Lampa.Storage.field(key);
    p.render(key).enter();
    assert.equal(p.Lampa.Storage.field(key), !before);
  }
});

test('adult approval requires confirmation, supports denial/back and can be revoked', () => {
  const p = page();
  const item = p.render('adult_content_view');
  assert.equal(item.find('.settings-param__value').value, 'No');
  item.enter();
  assert.equal(p.items.adult_content_view, undefined);
  p.modals.at(-1).buttons[1].onSelect();
  assert.equal(p.items.adult_content_view, undefined);
  item.enter(); p.modals.at(-1).onBack();
  assert.equal(p.items.adult_content_view, undefined);
  item.enter(); p.modals.at(-1).buttons[0].onSelect();
  assert.equal(p.items.adult_content_view, 'true');
  assert.equal(item.find('.settings-param__value').value, 'Yes');
  item.enter();
  assert.equal(p.items.adult_content_view, 'false');
  assert.equal(p.modals.length, 3, 'revoking approval needs no confirmation');
  assert.equal(p.controllers.at(-1), 'settings_component');
});

test('synced adult approval updates the row and child profiles cannot approve it', () => {
  const p = page();
  const item = p.render('adult_content_view');
  p.Lampa.Storage.set('adult_content_view', 'true');
  assert.equal(item.find('.settings-param__value').value, 'Yes');
  const child = page({child: true});
  assert.equal(child.render('adult_content_view').hidden, true);
  item.enter();
  item.enter();
  const modal = p.modals.at(-1);
  p.Lampa.Account.Permit.child = true;
  modal.buttons[0].onSelect();
  assert.equal(p.items.adult_content_view, 'false', 'profile changes are checked again on confirmation');
});

test('the original CUB mirror row is revealed and uses fresh mirror choices and failover state', () => {
  const p = page();
  p.Lampa.Manifest.cub_mirrors.push('custom.example');
  const item = p.mirror();
  assert.equal(item.hidden, false);
  assert.equal(p.params.some(p => p.param.name === 'cub_domain'), false, 'no duplicate selector');
  item.enter();
  const select = p.selections.at(-1);
  assert.ok(select.items.some(item => item.value === 'custom.example'));
  select.onSelect(select.items.find(item => item.value === 'cub.black'));
  assert.equal(p.items.cub_domain, 'cub.black');
  assert.equal(p.items.cub_alive, 'https://cub.black');
  assert.equal(p.writes.at(-1).nolisten, true);
});

test('disabled builds and unsupported origins keep their settings restrictions', () => {
  for (const flags of [{iptv: true}, {demo: true}, {white_use: true}]) assert.equal(page({flags}).params.length, 0);
  for (const protocol of ['file:', 'data:']) assert.equal(page({protocol}).params.length, 0);
  const p = page({flags: {account_use: false}});
  assert.equal(p.mirror().hidden, true);
  p.window.lampa_settings.demo = true;
  assert.equal(p.render('subtitles_size').hidden, true);
});

test('portable preferences sync, while subtitle size and CUB network selection stay local', () => {
  const scope = JSON.parse(fs.readFileSync(path.join(__dirname, '../svtlv/storage-scope.json'), 'utf8')).scopes;
  for (const key of ['adult_content_view', 'subtitles_stroke', 'subtitles_backdrop', 'player_segments_skip', 'player_segments_ad']) {
    assert.ok(scope.user.includes(key), key);
  }
  assert.ok(scope.device.includes('subtitles_size'));
  assert.ok(scope.connection.includes('cub_domain'));
});
