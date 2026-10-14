/*
 * Svtlv TorrServer membership (Plan 3). ES5, public Lampa APIs only.
 * Lists intersect TorrServer with account rows or a device-local guest map.
 * Removal never deletes from TorrServer; shared viewed marks are disabled.
 * Load before the bundle: the Lampa publication hook protects the first render.
 * Guest writes re-read storage, but simultaneous cross-tab writes are best-effort.
 */
(function () {
  'use strict';

  if (!/^https?:$/.test(window.location.protocol)) return;
  if (window.svtlv_torrserver_loaded) return;
  window.svtlv_torrserver_loaded = true;

  var lampa;
  var installed = false;
  var GUEST_KEY = 'svtlv_torrserver_guest_hashes';
  var strings = {
    en: {
      unavailable: 'My Torrents is unavailable. Try again later.',
      save: 'Could not save this torrent to My Torrents.',
      remove: 'Could not remove this torrent from My Torrents.',
      configure: 'Configure TorrServer before removing a torrent.'
    },
    ru: {
      unavailable: 'Мои торренты недоступны. Попробуйте позже.',
      save: 'Не удалось сохранить торрент в Мои торренты.',
      remove: 'Не удалось убрать торрент из Моих торрентов.',
      configure: 'Настройте TorrServer перед удалением торрента.'
    }
  };

  function notify(key) {
    try {
      var dict = strings[lampa.Storage.get('language', 'ru')] || strings.en;
      lampa.Noty.show(dict[key]);
    } catch (err) {
      // An unavailable UI must not restore the upstream shared list/removal.
    }
  }

  function call(fn, args, receiver) {
    if (typeof fn == 'function') fn.apply(receiver, args || []);
  }

  function canonical(hash) {
    if (typeof hash != 'string') return '';
    hash = hash.replace(/^\s+|\s+$/g, '').toLowerCase();
    return /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(hash) ? hash : '';
  }

  // Synchronous SHA-256 avoids both short-hash collisions and Web Crypto/Promise
  // dependencies on old TVs. Input is UTF-8; the result fits the API's 64-byte id.
  function digest(text) {
    var bytes = unescape(encodeURIComponent(text));
    var words = [];
    var length = bytes.length;
    var h = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    var k = [0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
      0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
      0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
      0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
      0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
      0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
      0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
      0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
      0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2];
    var i, j, w, a, b, c, d, e, f, g, v, t1, t2;
    function rotate(x, n) { return (x >>> n) | (x << (32 - n)); }

    for (i = 0; i < length; i++) {
      words[i >> 2] = (words[i >> 2] || 0) | (bytes.charCodeAt(i) << (24 - (i % 4) * 8));
    }
    words[length >> 2] = (words[length >> 2] || 0) | (0x80 << (24 - (length % 4) * 8));
    words[(((length + 8) >> 6) + 1) * 16 - 1] = length * 8;
    for (i = 0; i < words.length; i += 16) {
      w = [];
      a = h[0]; b = h[1]; c = h[2]; d = h[3];
      e = h[4]; f = h[5]; g = h[6]; v = h[7];
      for (j = 0; j < 64; j++) {
        if (j < 16) w[j] = words[i + j] || 0;
        else {
          t1 = w[j - 15]; t2 = w[j - 2];
          w[j] = (w[j - 16] + (rotate(t1, 7) ^ rotate(t1, 18) ^ (t1 >>> 3)) +
            w[j - 7] + (rotate(t2, 17) ^ rotate(t2, 19) ^ (t2 >>> 10))) | 0;
        }
        t1 = (v + (rotate(e, 6) ^ rotate(e, 11) ^ rotate(e, 25)) +
          ((e & f) ^ (~e & g)) + k[j] + w[j]) | 0;
        t2 = ((rotate(a, 2) ^ rotate(a, 13) ^ rotate(a, 22)) +
          ((a & b) ^ (a & c) ^ (b & c))) | 0;
        v = g; g = f; f = e; e = (d + t1) | 0;
        d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      w = [a, b, c, d, e, f, g, v];
      for (j = 0; j < 8; j++) h[j] = (h[j] + w[j]) | 0;
    }
    return h.map(function (word) {
      return ('00000000' + (word >>> 0).toString(16)).slice(-8);
    }).join('');
  }

  function serverId() {
    var url = lampa.Storage.get('torrserver_url');
    if (typeof url != 'string') return '';
    url = url.replace(/^\s+|\s+$/g, '').toLowerCase().replace(/^https?:\/\//, '').replace(/\/+$/, '');
    return url ? digest(url) : '';
  }

  function capture() {
    var state = window.svtlv_account_state || {};
    return {status: state.status, generation: state.generation, user_id: state.user_id, server: serverId()};
  }

  function current(ctx) {
    var now = capture();
    return now.status == ctx.status && now.generation === ctx.generation &&
      now.user_id === ctx.user_id && now.server == ctx.server;
  }

  function known(ctx) {
    return ctx.status == 'guest' || (ctx.status == 'signed_in' && !!ctx.user_id);
  }

  function active() {
    try { return lampa.Activity.active(); } catch (err) { return null; }
  }

  function refresh(origin) {
    // Back reuses retained components without fetching their list again. Discard
    // inactive lists now so their old cards cannot render under a new scope.
    if (!origin && lampa.Activity.all) {
      var here = active();
      lampa.Activity.all().forEach(function (item) {
        if (item !== here && item.component == 'mytorrents' && item.activity) {
          item.activity.destroy();
          item.activity = null;
        }
      });
    }
    // Always defer: the upstream long-press handler disables its card after remove().
    setTimeout(function () {
      var here = active();
      if (!here || here.component != 'mytorrents' || (origin && here !== origin)) return;
      try { lampa.Activity.replace(); } catch (err) { notify('unavailable'); }
    }, 0);
  }

  function expire(ctx) {
    if (!current(ctx)) return;
    // Block immediately even if account.js is not listening yet.
    window.svtlv_account_state = {status: 'checking', generation: ctx.generation + 1, user_id: ''};
    refresh();
    lampa.Listener.send('svtlv_account', {type: 'expired'});
  }

  function request(ctx, method, hash, done) {
    try {
      var headers = {'X-Lampa-Expected-User': ctx.user_id};
      if (method != 'GET') headers['X-Lampa-Csrf'] = '1';
      return $.ajax({
        url: '/api/v1/torrservers/' + ctx.server + '/hashes' + (hash ? '/' + hash : ''),
        type: method,
        dataType: method == 'GET' ? 'json' : 'text',
        cache: false,
        timeout: 15000,
        headers: headers,
        success: function (data, status, xhr) {
          if (!current(ctx)) return;
          if (xhr && xhr.status != (method == 'GET' ? 200 : 204)) { done(false); return; }
          done(true, data);
        },
        error: function (xhr) {
          if (!current(ctx)) return;
          // A different tab can replace the shared cookie without updating this
          // page's snapshot. A mismatch must recheck identity, never retry a write.
          var expired = xhr && (xhr.status == 401 || xhr.status == 409);
          if (expired) expire(ctx);
          done(false, {expired: expired});
        }
      });
    } catch (err) {
      done(false);
    }
  }

  function guestMap() {
    var raw = window.localStorage.getItem(GUEST_KEY);
    var data = raw === null ? {version: 1, servers: {}} : JSON.parse(raw);
    if (!data || data.version !== 1 || !data.servers ||
      Object.prototype.toString.call(data.servers) != '[object Object]') throw new Error('guest map');
    Object.keys(data.servers).forEach(function (server) {
      var hashes = data.servers[server];
      if (!/^[0-9a-f]{64}$/.test(server) || !Array.isArray(hashes)) throw new Error('guest server');
      hashes.forEach(function (hash) {
        if (!canonical(hash) || canonical(hash) !== hash) throw new Error('guest hash');
      });
    });
    return data;
  }

  function guestWrite(ctx, hash, remove) {
    // Never cache this map: another tab may have updated another server/hash.
    var data = guestMap();
    var hashes = data.servers[ctx.server] || [];
    var index = hashes.indexOf(hash);
    if (remove && index >= 0) hashes.splice(index, 1);
    if (!remove && index < 0) hashes.push(hash);
    data.servers[ctx.server] = hashes;
    var raw = JSON.stringify(data);
    window.localStorage.setItem(GUEST_KEY, raw);
    if (window.localStorage.getItem(GUEST_KEY) !== raw) throw new Error('guest write');
  }

  function install(api) {
    if (installed || !api || !api.Torserver || !api.Storage || !api.Listener) return;
    installed = true;
    lampa = api;
    var original = {my: api.Torserver.my, add: api.Torserver.add, hash: api.Torserver.hash};

    api.Torserver.my = function (success, fail) {
      var ctx, hashes, torrents;
      var membersReady = false;
      var torrentsReady = false;
      var settled = false;
      function reject(notice, args, receiver) {
        if (settled) return;
        settled = true;
        if (notice) notify('unavailable');
        call(fail, args, receiver);
      }
      function finish() {
        if (settled || !current(ctx) || !membersReady || !torrentsReady) return;
        settled = true;
        var filtered = torrents.filter(function (item) {
          return item && canonical(item.hash) && hashes.indexOf(canonical(item.hash)) >= 0;
        });
        if (filtered.length) call(success, [filtered]);
        else call(fail);
      }
      try {
        ctx = capture();
        if (!known(ctx)) { reject(true); return; }
        if (!ctx.server) {
          // Preserve upstream's unconfigured path, but never expose its result.
          original.my.call(api.Torserver, function () { reject(false); }, function () { reject(false, arguments, this); });
          return;
        }
        if (ctx.status == 'guest') {
          hashes = guestMap().servers[ctx.server] || [];
          membersReady = true;
        } else {
          request(ctx, 'GET', '', function (ok, data) {
            if (!ok || !data || !Array.isArray(data.hashes) || data.hashes.some(function (hash) { return !canonical(hash); })) {
              reject(true); return;
            }
            hashes = data.hashes.map(canonical);
            membersReady = true;
            finish();
          });
        }
        // No delayed original call: Torserver.clear() keeps its existing ordering.
        original.my.call(api.Torserver, function (result) {
          if (!current(ctx) || settled) return;
          if (!Array.isArray(result)) { reject(true); return; }
          torrents = result;
          torrentsReady = true;
          finish();
        }, function () {
          if (current(ctx)) reject(false, arguments, this);
        });
      } catch (err) { reject(true); }
    };

    function wrapAdd(name) {
      api.Torserver[name] = function (object, success, fail) {
        var ctx;
        var saved;
        try {
          ctx = capture();
          saved = name == 'add' || api.Storage.field('torrserver_savedb');
        } catch (err) { notify('save'); call(fail); return; }
        if (!known(ctx)) { notify('save'); call(fail); return; }
        function failed(args, receiver) {
          if (current(ctx)) call(fail, args, receiver);
        }
        try {
          return original[name].call(api.Torserver, object, function (json) {
            var args = arguments;
            var receiver = this;
            if (!current(ctx)) return;
            if (!ctx.server || !saved) { call(success, args, receiver); return; }
            var hash = json && canonical(json.hash);
            function recorded(ok, error) {
              if (!current(ctx) && !(error && error.expired)) return;
              if (!ok) notify('save');
              // Playback starts immediately below; explicit Add's success toast
              // must wait for persistence and must never claim a failed save.
              if (name == 'add') {
                if (ok) call(success, args, receiver);
                else call(fail);
              }
            }
            if (!hash) recorded(false);
            else if (ctx.status == 'signed_in') request(ctx, 'PUT', hash, recorded);
            else {
              try { guestWrite(ctx, hash, false); recorded(true); }
              catch (err) { recorded(false); }
            }
            if (name == 'hash' && current(ctx)) call(success, args, receiver);
          }, function () { failed(arguments, this); });
        } catch (err) { notify('save'); failed([]); }
      };
    }
    wrapAdd('add');
    wrapAdd('hash');

    api.Torserver.remove = function (hash, success, fail) {
      var origin = active();
      var ctx;
      function done(ok, args, receiver) {
        if (ok) call(success, args, receiver);
        else {
          notify(ctx && !ctx.server ? 'configure' : 'remove');
          call(fail, args, receiver);
          refresh(origin);
        }
      }
      try {
        ctx = capture();
        hash = canonical(hash);
        if (!known(ctx) || !ctx.server || !hash) { done(false); return; }
        if (ctx.status == 'signed_in') {
          request(ctx, 'DELETE', hash, function (ok) { done(ok); });
        } else {
          guestWrite(ctx, hash, true);
          done(true);
        }
      } catch (err) { done(false); }
    };

    api.Torserver.viewed = function (hash, success, fail) {
      setTimeout(function () { call(fail); }, 0);
    };
    api.Torserver.viewedSet = function () {};

    api.Listener.follow('svtlv_account', function (event) {
      if (event.type == 'state') refresh();
    });
    if (api.Storage.listener) api.Storage.listener.follow('change', function (event) {
      if (event.name == 'torrserver_url') refresh();
    });
    $(window).on('storage', function (event) {
      var change = event.originalEvent || event;
      if ((!change.key || change.key == GUEST_KEY) && (window.svtlv_account_state || {}).status == 'guest') refresh();
    });
    refresh();
  }

  function poll() {
    if (installed) return;
    install(window.Lampa);
    if (!installed) setTimeout(poll, 200);
  }

  // The loader waits for this file before starting the bundle. Install during its
  // public API assignment, before prepareApp/loadApp can restore any activity.
  if (!window.Lampa) {
    try {
      Object.defineProperty(window, 'Lampa', {
        configurable: true,
        enumerable: true,
        get: function () { return undefined; },
        set: function (api) {
          Object.defineProperty(window, 'Lampa', {value: api, writable: true, configurable: true, enumerable: true});
          install(api);
        }
      });
    } catch (err) {
      // Very old engines without window accessors still get early polling.
    }
  }
  poll();
})();
