/*
 * Svtlv Account storage sync (Plan 4). ES5, public Lampa APIs only.
 * The runtime allowlist owns export and encryption policy. Values stay raw
 * localStorage strings so upstream readers keep their existing conversions.
 */
(function () {
  'use strict';

  if (!/^https?:$/.test(window.location.protocol)) return;
  if (window.svtlv_sync_loaded) return;
  window.svtlv_sync_loaded = true;

  var BASE_KEY = 'svtlv_sync_base';
  var USER_KEY = 'svtlv_sync_user';
  var MAX_BYTES = 4 * 1024 * 1024;
  var KEY_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;
  var SCOPE_NAMES = ['user', 'user_connection', 'user_optional', 'device', 'connection', 'external', 'cache'];
  var scope;
  var base;
  var baseRaw;
  var disabled = false;
  var failure = '';
  var pendingNotice = false;
  var bootCalled = false;
  var ready = false;
  var readyUser = '';
  var lastDoc = {};
  var lastEtag = '';
  var unreadableKeys = [];
  var accountUser = '';
  var handledUser = '';
  var accountRevision = 0;
  var uploads = false;
  var reloading = false;
  var applying = false;
  var clearing = false;
  var saveTimer = null;
  var saveDelay = 0;
  var saving = false;
  var saveAgain = false;
  var saveFailure = false;
  var pollTimer = null;
  var pollFlight = null;
  var strings = {
    en: {save: 'Not saved to Account', storage: 'Account sync is unavailable on this device'},
    ru: {save: 'Не сохранено в Аккаунт', storage: 'Синхронизация Аккаунта недоступна на этом устройстве'}
  };

  function own(obj, key) {
    return Object.prototype.hasOwnProperty.call(obj, key);
  }

  function record(value) {
    return value !== null && typeof value == 'object' && !Array.isArray(value);
  }

  function notify(key) {
    failure = key;
    pendingNotice = true;
    try {
      var lampa = window.Lampa;
      if (lampa && lampa.Noty) {
        var language = lampa.Storage.get('language', 'ru');
        lampa.Noty.show((strings[language] || strings.en)[key]);
        pendingNotice = false;
      }
    } catch (err) {
      // Remember pre-boot errors; a missing UI cannot prevent Lampa starting.
    }
  }

  function showPendingNotice() {
    if (!pendingNotice) return;
    notify(failure);
    if (pendingNotice) setTimeout(showPendingNotice, 100);
  }

  function disable() {
    if (disabled) return;
    disabled = true;
    uploads = false;
    stopSave();
    stopPolling();
    notify('storage');
  }

  function quota(err) {
    return err && (err.name == 'QuotaExceededError' || err.name == 'NS_ERROR_DOM_QUOTA_REACHED' ||
      err.code === 22 || err.code === 1014);
  }

  // Even obtaining localStorage, length or key() can throw in a TV WebView.
  function storage(method, key, value, optional) {
    // Disabled synchronization must still allow ownership cleanup on logout.
    if (disabled && !clearing) throw new Error('Account sync disabled');
    try {
      var store = window.localStorage;
      if (method == 'length') return store.length;
      if (method == 'setItem') {
        store.setItem(key, value);
        if (store.getItem(key) !== value) throw new Error('Storage write failed');
        return true;
      }
      if (method == 'removeItem') {
        store.removeItem(key);
        if (store.getItem(key) !== null) throw new Error('Storage removal failed');
        return true;
      }
      return store[method](key);
    } catch (err) {
      if (optional && quota(err)) return false;
      disable();
      throw err;
    }
  }

  // The same helper supports scope GETs, conditional document GETs and PUTs.
  function request(method, url, options, done) {
    options = options || {};
    if (disabled) { done(false, null, {status: 0}); return; }
    var headers = {};
    if (options.user) headers['X-Lampa-Expected-User'] = options.user;
    if (method != 'GET' && method != 'HEAD') headers['X-Lampa-Csrf'] = '1';
    if (options.etag) headers[method == 'GET' ? 'If-None-Match' : 'If-Match'] = options.etag;
    try {
      return $.ajax({
        url: url,
        type: method,
        dataType: method == 'GET' ? 'json' : 'text',
        contentType: 'application/json; charset=UTF-8',
        processData: false,
        timeout: options.timeout || 15000,
        headers: headers,
        data: options.body === undefined ? undefined : JSON.stringify(options.body),
        success: function (data, status, xhr) { done(true, data, xhr); },
        error: function (xhr) { done(false, null, xhr); }
      });
    } catch (err) {
      done(false, null, {status: 0});
    }
  }

  // A sibling can save or switch accounts while a GET is in flight. Check the
  // owner before retrying an advanced baseline so old-account data stays isolated.
  function requestDocument(options, active, done) {
    var snapshot, owner;
    try {
      snapshot = storage('getItem', BASE_KEY);
      owner = storage('getItem', USER_KEY);
    }
    catch (err) { done(false, null, {status: 0}); return; }
    var answered = false;
    request('GET', '/api/v1/user-data', options, function (ok, data, xhr) {
      if (answered || !active()) return;
      answered = true;
      try {
        if (owner !== storage('getItem', USER_KEY)) {
          done(false, null, {status: 0});
          return;
        }
        if (snapshot !== storage('getItem', BASE_KEY)) {
          requestDocument(options, active, done);
          return;
        }
      } catch (err) { done(false, null, {status: 0}); return; }
      done(ok, data, xhr, snapshot);
    });
  }

  function validateScope(value) {
    if (!record(value) || value.version !== 1 || !record(value.scopes) ||
      Object.keys(value.scopes).length !== SCOPE_NAMES.length) throw new Error('Invalid scope');
    var entries = Object.create(null);
    SCOPE_NAMES.forEach(function (name) {
      if (!own(value.scopes, name) || !Array.isArray(value.scopes[name])) throw new Error('Invalid scope');
      value.scopes[name].forEach(function (entry) {
        if (typeof entry != 'string' || !/^[A-Za-z0-9_.:-]+\*?$/.test(entry) || own(entries, entry)) {
          throw new Error('Invalid scope rule');
        }
        entries[entry] = name;
      });
    });
    var encrypt = value.encrypt === undefined ? [] : value.encrypt;
    if (!Array.isArray(encrypt) || encrypt.length > 64) throw new Error('Invalid encrypt list');
    encrypt.forEach(function (key) {
      if (typeof key != 'string' || !KEY_PATTERN.test(key)) throw new Error('Invalid encrypt key');
    });
    return {entries: entries, encrypt: encrypt.slice()};
  }

  function loadScope(done, active) {
    var version = typeof cache_version == 'undefined' ? Math.floor(new Date().getTime() / 9e5) : cache_version;
    return request('GET', 'svtlv/storage-scope.json?v=' + version, {}, function (ok, data) {
      if (active && !active()) return;
      if (disabled) { done(false); return; }
      if (!ok) { done(false); return; }
      try { scope = validateScope(data); }
      catch (err) { disabled = true; notify('save'); done(false); return; }
      done(true);
    });
  }

  function errorCode(xhr) {
    try {
      var body = xhr.responseJSON || JSON.parse(xhr.responseText);
      return body && body.error && body.error.code;
    } catch (err) { return ''; }
  }

  function rememberDoc(data, xhr) {
    var missing = data.unreadable_keys === undefined ? [] : data.unreadable_keys;
    if (!Array.isArray(missing) || missing.some(function (key) {
      return typeof key != 'string' || !KEY_PATTERN.test(key) || own(data.data, key);
    })) throw new Error('Invalid unreadable keys');
    lastDoc = data.data;
    lastEtag = xhr.getResponseHeader('ETag') || '';
    unreadableKeys = missing.slice();
  }

  // Run before the bundle: no Lampa API or account event is available yet.
  window.svtlv_sync_boot = function (done) {
    if (bootCalled) return;
    bootCalled = true;
    var finished = false;
    var scopeDone = false, scopeOK = false, dataDone = false;
    var result;
    // Arm the deadline before even reading localStorage or starting requests.
    var timer = setTimeout(function () {
      if (finished) return;
      notify('save');
      finish();
    }, 3000);

    function finish() {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      try { done(); }
      finally {
        if (pendingNotice) setTimeout(showPendingNotice, 0);
      }
    }

    function settle() {
      if (finished || !scopeDone || !dataDone) return;
      try {
        // The owner may have changed after the response while scope was loading.
        if (marker !== storage('getItem', USER_KEY)) { finish(); return; }
        if (!scopeOK) {
          if (!failure) notify('save');
        } else if (result.snapshot !== storage('getItem', BASE_KEY)) {
          // The document may have arrived before the scope, then become stale.
          loadData();
          return;
        } else if (result.xhr.status == 401 || result.xhr.status == 409) {
          clearSynced();
        } else if (result.ok && result.xhr.status == 200 && record(result.data) &&
          result.data.schema_version === 2 && record(result.data.data)) {
          applyDoc(result.data.data);
          rememberDoc(result.data, result.xhr);
          ready = true;
          readyUser = marker;
        } else if (result.xhr.status == 404 && errorCode(result.xhr) == 'user_data_not_found') {
          lastDoc = {};
          lastEtag = '';
          unreadableKeys = [];
          ready = true;
          readyUser = marker;
        } else {
          notify('save');
        }
      } catch (err) {
        if (!disabled) notify('save');
      }
      finish();
    }

    function loadData() {
      dataDone = false;
      requestDocument({user: marker, timeout: 3000}, function () { return !finished && !dataDone; },
        function (ok, data, xhr, snapshot) {
          if (finished || dataDone) return;
          dataDone = true;
          result = {ok: ok, data: data, xhr: xhr, snapshot: snapshot};
          settle();
        });
    }

    try {
      var marker = storage('getItem', USER_KEY);
      if (!marker) { finish(); return; }
      loadScope(function (ok) {
        if (finished || scopeDone) return;
        scopeDone = true;
        scopeOK = ok;
        settle();
      }, function () { return !finished; });
      loadData();
    } catch (err) {
      if (!disabled) notify('save');
      finish();
    }
  };

  function classify(key) {
    if (!scope || typeof key != 'string') return '';
    if (own(scope.entries, key)) return scope.entries[key];
    var found = '', length = -1;
    Object.keys(scope.entries).forEach(function (entry) {
      if (entry.charAt(entry.length - 1) != '*') return;
      var prefix = entry.slice(0, -1);
      if (prefix.length > length && key.indexOf(prefix) === 0) {
        found = scope.entries[entry];
        length = prefix.length;
      }
    });
    return found;
  }

  function synced(key) {
    if (typeof key != 'string' || !KEY_PATTERN.test(key)) return false;
    var name = classify(key);
    return name == 'user' || name == 'user_connection' || name == 'user_optional';
  }

  function fp(value) {
    if (value === null || value === '') return null;
    var hash = 2166136261;
    for (var i = 0; i < value.length; i++) {
      hash ^= value.charCodeAt(i);
      // FNV-1a multiplication modulo 2^32 without Math.imul (ES6).
      hash += (hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24);
    }
    return value.length + ':' + (hash >>> 0).toString(16);
  }

  function readBase() {
    try {
      var raw = storage('getItem', BASE_KEY);
      // Other tabs save into the same storage even before their storage event arrives.
      if (base && raw === baseRaw) return base;
      var parsed = raw ? JSON.parse(raw) : {};
      if (!record(parsed)) throw new Error('Invalid sync base');
      base = Object.create(null);
      Object.keys(parsed).forEach(function (key) {
        if (typeof parsed[key] != 'string' || !/^\d+:[0-9a-f]+$/.test(parsed[key])) {
          throw new Error('Invalid fingerprint');
        }
        base[key] = parsed[key];
      });
      baseRaw = raw;
      return base;
    } catch (err) {
      disable();
      throw err;
    }
  }

  function writeBase() {
    var raw = JSON.stringify(base || readBase());
    storage('setItem', BASE_KEY, raw);
    baseRaw = raw;
  }

  function localValue(key) {
    var value = storage('getItem', key);
    // Upstream caches both caller defaults and quota-failed values in memory.
    // Persist a missing must-sync value before exporting it; only an actual
    // write failure disables sync, and a reserve value never becomes a deletion.
    if (value === null && classify(key) != 'user_optional' && window.Lampa && window.Lampa.Storage) {
      var fallback;
      try {
        fallback = window.Lampa.Storage.get(key, '');
        if (fallback !== '' && fallback !== null && fallback !== undefined) {
          value = record(fallback) || Array.isArray(fallback) ? JSON.stringify(fallback) : String(fallback);
          storage('setItem', key, value);
        }
      } catch (err) { disable(); throw err; }
    }
    return value;
  }

  function dirty(key) {
    var previous = readBase();
    return fp(localValue(key)) !== (own(previous, key) ? previous[key] : null);
  }

  function localKeys() {
    var keys = [];
    var length = storage('length');
    for (var i = 0; i < length; i++) {
      var key = storage('key', i);
      if (synced(key)) keys.push(key);
    }
    return keys;
  }

  function exportLocal() {
    var data = Object.create(null);
    localKeys().forEach(function (key) {
      var value = localValue(key);
      if (value !== null && value !== '') data[key] = value;
    });
    return data;
  }

  function overlay(lastDoc) {
    var data = Object.create(null);
    Object.keys(lastDoc).forEach(function (key) {
      if (synced(key) && typeof lastDoc[key] == 'string') data[key] = lastDoc[key];
    });
    dirtyKeys().forEach(function (key) {
      var value = localValue(key);
      // A missing local item also needs a tombstone to clear other devices.
      data[key] = value === null ? '' : value;
    });
    return data;
  }

  function writeLive(key, value, optional, raw) {
    var parsed = value;
    if (!raw && (value.charAt(0) == '[' || value.charAt(0) == '{')) {
      try { parsed = JSON.parse(value); } catch (err) { /* Match Storage.get's raw fallback. */ }
    }
    try {
      var writeError;
      window.Lampa.Storage.set(key, parsed, false, function (err) { writeError = err; });
      if (writeError) throw writeError;
      var encoded = record(parsed) || Array.isArray(parsed) ? JSON.stringify(parsed) : value;
      if (storage('getItem', key) !== encoded) throw new Error('Storage write failed');
      // Storage.set refreshes the memory cache but normalizes JSON whitespace.
      // Keep the server's exact raw string for export and fingerprint comparison.
      if (encoded !== value) return storage('setItem', key, value, optional);
      return true;
    } catch (err) {
      if (optional && quota(err)) return false;
      disable();
      throw err;
    }
  }

  function refreshLive(written) {
    var lampa = window.Lampa;
    if (written.favorite && lampa.Favorite) lampa.Favorite.read();
    if (lampa.Timeline && written[lampa.Timeline.filename()]) lampa.Timeline.read();
  }

  function applyDoc(data, live) {
    var previous = readBase();
    var changed = false;
    var recover = false;
    var written = Object.create(null);
    var keys = Object.keys(data).filter(function (key) {
      return synced(key) && typeof data[key] == 'string';
    });
    keys.sort(function (a, b) {
      return (classify(a) == 'user_optional' ? 1 : 0) - (classify(b) == 'user_optional' ? 1 : 0);
    });
    if (live) applying = true;
    try {
      scope.encrypt.forEach(function (key) {
        if (synced(key) && !own(data, key) && own(previous, key) && !dirty(key)) {
          // An unreadable sealed field is absent on GET. Keep its plaintext,
          // but stop treating it as saved so this device can seal it again.
          delete previous[key];
          recover = true;
        }
      });
      keys.forEach(function (key) {
        if (dirty(key)) return;
        var value = data[key];
        var differs = storage('getItem', key) !== value;
        // Storage and module caches belong to this tab, unlike localStorage/base.
        // A sibling may already have persisted a value this tab has never applied.
        if (differs || (live && (!own(lastDoc, key) || lastDoc[key] !== value))) {
          var optional = classify(key) == 'user_optional';
          if (!(live ? writeLive(key, value, optional) : storage('setItem', key, value, optional))) return;
          if (differs) changed = true;
          written[key] = true;
        }
        if (fp(value) === null) delete previous[key];
        else previous[key] = fp(value);
      });
      writeBase();
      if (live) refreshLive(written);
      return {changed: changed};
    } finally {
      if (live) {
        applying = false;
        // A running save already includes recovered values in its overlay.
        if (recover && !saving) scheduleSave();
      }
    }
  }

  function clearSynced() {
    var wasClearing = clearing;
    clearing = true;
    try {
      var keys = Object.create(null);
      var changed = false;
      localKeys().forEach(function (key) { keys[key] = true; });
      var previous = {};
      try { previous = readBase(); }
      catch (err) { /* A corrupt base cannot prevent clearing known account keys. */ }
      Object.keys(previous).forEach(function (key) { if (synced(key)) keys[key] = true; });
      // Exact rules also mask reserve-only copies with an empty local item.
      Object.keys(scope.entries).forEach(function (key) { if (synced(key)) keys[key] = true; });
      Object.keys(keys).forEach(function (key) {
        if (storage('getItem', key) !== '') {
          storage('setItem', key, '');
          changed = true;
        }
      });
      storage('removeItem', BASE_KEY);
      storage('removeItem', USER_KEY);
      base = Object.create(null);
      baseRaw = null;
      return changed;
    } finally { clearing = wasClearing; }
  }

  function reloadPage() {
    if (reloading) return;
    reloading = true;
    uploads = false;
    stopSave();
    stopPolling();
    window.location.reload();
  }

  // Clear guest/previous-account data even when the server omits a key. Compare
  // final values so a temporary clear + identical apply does not reload the TV.
  function replaceDoc(data, user) {
    var before = exportLocal();
    var reserve = false;
    Object.keys(scope.entries).forEach(function (key) {
      if (synced(key) && storage('getItem', key) === null) {
        var value = window.Lampa.Storage.get(key, '');
        if (value !== '' && value !== null && value !== undefined) reserve = true;
      }
    });
    clearSynced();
    // A failed import must retain an owner so logout/account replacement can
    // clear partial plaintext, including when writing its fingerprints fails.
    storage('setItem', USER_KEY, user);
    applyDoc(data);
    var after = exportLocal();
    return reserve || Object.keys(before).some(function (key) { return before[key] !== after[key]; }) ||
      Object.keys(after).some(function (key) { return before[key] !== after[key]; });
  }

  function handleAccount(state) {
    if (reloading || !state) return;
    if (state.status != 'guest' && state.status != 'signed_in') return;
    var user = state.status == 'signed_in' ? state.user_id : '';
    if (state.status == 'signed_in' && (typeof user != 'string' || !user)) return;
    if (disabled) {
      clearing = true;
      try {
        var marker = storage('getItem', USER_KEY);
        if (scope && marker && marker !== user) {
          clearSynced();
          if (!window.svtlv_account_leaving) reloadPage();
        }
      } catch (err) { /* A storage failure cannot escape the account listener. */ }
      finally { clearing = false; }
      return;
    }
    if (user && (user == handledUser || user == accountUser)) return;

    var revision = ++accountRevision;
    accountUser = user;
    handledUser = '';
    uploads = false;
    stopSave();
    stopPolling();
    function active() { return !disabled && !reloading && revision == accountRevision; }
    function failed() {
      accountUser = '';
      if (!disabled) notify('save');
    }
    function complete() {
      if (!active()) return;
      try {
        var marker = storage('getItem', USER_KEY);
        if (!user) {
          ready = false;
          readyUser = '';
          lastDoc = {};
          lastEtag = '';
          unreadableKeys = [];
          if (marker) {
            clearSynced();
            if (!window.svtlv_account_leaving) reloadPage();
          }
          return;
        }
        if (marker == user && ready && readyUser == user) {
          handledUser = user;
          enableUploads();
          return;
        }
        requestDocument({user: user}, active, function (ok, data, xhr) {
          if (!active()) return;
          try {
            var changed = false;
            if (ok && xhr.status == 200 && record(data) && data.schema_version === 2 && record(data.data)) {
              changed = marker == user ? applyDoc(data.data).changed : replaceDoc(data.data, user);
              rememberDoc(data, xhr);
            } else if (xhr.status == 404 && errorCode(xhr) == 'user_data_not_found') {
              if (marker && marker != user) changed = clearSynced();
              if (marker != user) {
                base = Object.create(null);
                writeBase();
              }
              lastDoc = {};
              lastEtag = '';
              unreadableKeys = [];
            } else if (xhr.status == 401 || xhr.status == 409) {
              accountUser = '';
              window.Lampa.Listener.send('svtlv_account', {type: 'expired'});
              return;
            } else {
              failed();
              return;
            }
            // Persist ownership before reload, including an empty document.
            storage('setItem', USER_KEY, user);
            ready = true;
            readyUser = user;
            handledUser = user;
            if (changed) reloadPage();
            else enableUploads();
          } catch (err) { failed(); }
        });
      } catch (err) { failed(); }
    }

    try {
      // Anonymous pages neither load the scope nor request user data.
      if (!user && !storage('getItem', USER_KEY)) {
        ready = false;
        readyUser = '';
        lastDoc = {};
        lastEtag = '';
        unreadableKeys = [];
        return;
      }
      if (scope) complete();
      else loadScope(function (ok) {
        if (!active()) return;
        if (ok) complete();
        else failed();
      }, active);
    } catch (err) { failed(); }
  }

  function whenReady() {
    if (!window.Lampa || !window.Lampa.Listener) {
      setTimeout(whenReady, 100);
      return;
    }
    window.Lampa.Listener.follow('svtlv_account', function (event) {
      if (event.type == 'state') handleAccount(event.state);
    });
    $(window).on('storage', function (event) {
      var change = event.originalEvent || event;
      if (change.key == USER_KEY && change.oldValue !== change.newValue) reloadPage();
      if (!uploads || disabled || reloading || !synced(change.key)) return;
      try {
        if (storage('getItem', USER_KEY) !== accountUser) return;
        // Events can queue behind later edits. Refresh from the current shared value.
        var value = storage('getItem', change.key);
        if (value === null) return;
        applying = true;
        // Write the raw string unchanged so JSON normalization cannot bounce
        // storage events between tabs. Storage.get parses it for module readers.
        if (writeLive(change.key, value, classify(change.key) == 'user_optional', true)) {
          var written = Object.create(null);
          written[change.key] = true;
          refreshLive(written);
        }
      } catch (err) { /* Persistence failures disable sync without breaking the page. */ }
      finally { applying = false; }
    });
    if (window.Lampa.Storage.listener) window.Lampa.Storage.listener.follow('change', function (event) {
      if (!applying && event && synced(event.name)) {
        try { localValue(event.name); scheduleSave(); }
        catch (err) { /* A persistence failure disables sync without disrupting the writer. */ }
      }
    });
    $(document).on('visibilitychange', function () {
      if (document.hidden) {
        clearTimeout(pollTimer);
        pollTimer = null;
        save();
      } else {
        poll();
        schedulePoll();
      }
    });
    handleAccount(window.svtlv_account_state);
  }

  function stopSave() {
    clearTimeout(saveTimer);
    saveTimer = null;
    saveDelay = 0;
    saving = false;
    saveAgain = false;
  }

  function dirtyKeys() {
    var keys = Object.create(null);
    localKeys().forEach(function (key) { keys[key] = true; });
    Object.keys(readBase()).forEach(function (key) { if (synced(key)) keys[key] = true; });
    // First-login reserve values have neither a local item nor a fingerprint.
    // Inspect exact must-sync rules so localValue can persist them or detect failure.
    Object.keys(scope.entries).forEach(function (key) {
      if (synced(key) && classify(key) != 'user_optional') keys[key] = true;
    });
    return Object.keys(keys).filter(dirty);
  }

  function enableUploads() {
    uploads = true;
    saveFailure = false;
    // Includes edits made with nolisten before a reload and first-login guest data.
    scheduleSave();
    schedulePoll();
  }

  function scheduleSave() {
    if (!uploads || disabled || reloading || applying) return;
    try {
      var keys = dirtyKeys();
      if (!keys.length) return;
      if (saving) { saveAgain = true; return; }
      var delay = keys.some(function (key) { return classify(key) != 'user_optional'; }) ? 2000 : 300000;
      // Frequent playback writes must not keep postponing an optional save.
      if (saveTimer !== null && saveDelay <= delay) return;
      clearTimeout(saveTimer);
      saveDelay = delay;
      saveTimer = setTimeout(function () {
        saveTimer = null;
        saveDelay = 0;
        save();
      }, delay);
    } catch (err) { /* Storage failures already disable sync. */ }
  }

  function saveError(xhr) {
    if (xhr.status == 401 || xhr.status == 409) {
      uploads = false;
      ready = false;
      readyUser = '';
      accountUser = '';
      handledUser = '';
      ++accountRevision;
      stopSave();
      stopPolling();
      window.Lampa.Listener.send('svtlv_account', {type: 'expired'});
      return;
    }
    if (!saveFailure && !disabled) notify('save');
    saveFailure = true;
  }

  function stopPolling() {
    clearTimeout(pollTimer);
    pollTimer = null;
    pollFlight = null;
  }

  function schedulePoll() {
    if (pollTimer !== null || document.hidden || !uploads || disabled || reloading) return;
    pollTimer = setTimeout(function () {
      pollTimer = null;
      if (document.hidden || !uploads || disabled || reloading) return;
      poll();
      schedulePoll();
    }, 60000);
  }

  // Saves join an in-flight poll; timer/visibility polls never overlap a PUT.
  function poll(done) {
    if (!uploads || disabled || reloading || (saving && !done)) return;
    if (pollFlight) {
      if (done) pollFlight.callbacks.push(done);
      return;
    }
    var flight = {revision: accountRevision, callbacks: done ? [done] : []};
    pollFlight = flight;
    var incomplete = unreadableKeys.length > 0;
    requestDocument({user: accountUser, etag: incomplete ? '' : lastEtag}, function () {
      return pollFlight === flight && flight.revision == accountRevision && uploads && !disabled && !reloading;
    }, function (ok, data, xhr) {
      if (pollFlight !== flight || flight.revision != accountRevision || !uploads || disabled || reloading) return;
      var success = false, missing = false;
      try {
        if (xhr.status == 304 && lastEtag) {
          success = true;
        } else if (ok && xhr.status == 200 && record(data) && data.schema_version === 2 && record(data.data)) {
          applyDoc(data.data, true);
          rememberDoc(data, xhr);
          success = true;
        } else if (xhr.status == 404 && errorCode(xhr) == 'user_data_not_found') {
          lastDoc = {};
          lastEtag = '';
          unreadableKeys = [];
          success = true;
          missing = true;
        } else {
          saveError(xhr);
        }
      } catch (err) { saveError({status: 0}); }
      if (pollFlight !== flight) return;
      pollFlight = null;
      if (success && !saving) {
        saveFailure = false;
        if (incomplete && !unreadableKeys.length) scheduleSave();
      }
      flight.callbacks.forEach(function (callback) { callback(success, missing); });
    });
  }

  function save() {
    if (!uploads || disabled || reloading || applying) return;
    if (saving) { saveAgain = true; return; }
    clearTimeout(saveTimer);
    saveTimer = null;
    saveDelay = 0;
    try { if (!dirtyKeys().length) return; }
    catch (err) { return; }
    saving = true;
    var revision = accountRevision;
    function active() { return revision == accountRevision && uploads && !disabled && !reloading; }
    function finish(ok) {
      if (!active()) return;
      saving = false;
      var again = saveAgain;
      saveAgain = false;
      if (ok) {
        saveFailure = false;
        if (again) save();
      }
    }
    function attempt(retry) {
      poll(function (ok, missing) {
        if (!active()) return;
        if (!ok) { finish(false); return; }
        try {
          // A missing server document needs every local value, even clean ones.
          var body = fitBody(overlay(missing ? exportLocal() : lastDoc));
          if (!body) { finish(false); return; }
          // A repair can preserve the plaintext ETag. Never replace an incomplete
          // document unless this device can supply every omitted synced value.
          if (unreadableKeys.some(function (key) { return synced(key) && !own(body.data, key); })) {
            saveError({status: 0});
            finish(false);
            return;
          }
          var sentBase = storage('getItem', BASE_KEY);
          request('PUT', '/api/v1/user-data', {user: accountUser, etag: lastEtag, body: body}, function (ok, data, xhr) {
            if (!active()) return;
            if (xhr.status == 412 && !retry) { attempt(true); return; }
            if (!ok || xhr.status != 204) {
              saveError(xhr);
              finish(false);
              return;
            }
            try {
              if (storage('getItem', USER_KEY) !== accountUser) { reloadPage(); return; }
              if (storage('getItem', BASE_KEY) !== sentBase) {
                // Another tab may have saved after this PUT committed, before its
                // acknowledgement arrived. Preserve its base and fetch current data.
                lastEtag = '';
                finish(true);
                poll(function (ok) { if (ok) scheduleSave(); });
                return;
              }
              var previous = readBase();
              Object.keys(body.data).forEach(function (key) {
                var fingerprint = fp(body.data[key]);
                if (fingerprint === null) delete previous[key];
                else previous[key] = fingerprint;
              });
              writeBase();
              lastDoc = body.data;
              lastEtag = xhr.getResponseHeader('ETag') || '';
              unreadableKeys = [];
              finish(true);
            } catch (err) { finish(false); }
          });
        } catch (err) {
          saveError({status: 0});
          finish(false);
        }
      });
    }
    attempt(false);
  }

  function encodedBytes(value) {
    // Check raw values as well: modern JSON.stringify escapes lone surrogates,
    // while old TV engines leave them literal and encodeURIComponent throws.
    encodeURIComponent(value);
    return encodeURIComponent(JSON.stringify(value)).replace(/%[0-9A-F]{2}/g, 'x').length;
  }

  function fitBody(data) {
    try {
      if (disabled || !scope) return null;
      var body = {schema_version: 2, data: Object.create(null), encrypt: scope.encrypt.slice()};
      Object.keys(data).forEach(function (key) {
        if (synced(key) && typeof data[key] == 'string') {
          encodedBytes(data[key]);
          body.data[key] = data[key];
        }
      });
      function size() { return encodeURIComponent(JSON.stringify(body)).replace(/%[0-9A-F]{2}/g, 'x').length; }
      var bytes = size();
      if (bytes <= MAX_BYTES) return body;
      var optional = Object.keys(body.data).filter(function (key) { return classify(key) == 'user_optional'; });
      optional.sort(function (a, b) { return encodedBytes(body.data[b]) - encodedBytes(body.data[a]); });
      for (var i = 0; i < optional.length && bytes > MAX_BYTES; i++) {
        delete body.data[optional[i]];
        bytes = size();
      }
      if (bytes <= MAX_BYTES) return body;
    } catch (err) {
      // Encoding/measurement failure leaves local edits dirty for a later save.
    }
    notify('save');
    return null;
  }

  whenReady();
})();
