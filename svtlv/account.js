/*
 * Svtlv Account add-on (Plan 2, design §10.5).
 *
 * Identity only: reads GET /api/v1/session and never calls the user-data API.
 * ES5 only (old TV browsers), public window.Lampa APIs only, never app.min.js
 * internals. Any failure leaves anonymous Lampa and CUB working as before.
 */
(function () {
  'use strict';

  var SESSION_URL = '/api/v1/session';
  var REQUEST_TIMEOUT = 15000;
  var HEARTBEAT_INTERVAL = 12 * 60 * 60 * 1000; // keeps the session cookie sliding on a TV left open
  var READY_POLL = 200;
  var COMPONENT = 'account_lampa';

  var ICON = '<svg height="169" viewBox="0 0 172 169" fill="none" xmlns="http://www.w3.org/2000/svg">' +
    '<circle cx="86" cy="60" r="22" stroke="white" stroke-width="12"/>' +
    '<path d="M42 136C42 111.7 61.7 92 86 92C110.3 92 130 111.7 130 136" stroke="white" stroke-width="12" stroke-linecap="round"/>' +
    '<circle cx="86" cy="84.5" r="78.5" stroke="white" stroke-width="12"/>' +
    '</svg>';

  var strings = {
    en: {
      title: 'Account',
      descr: 'Sign in with your Svtlv account. Settings, bookmarks and history stay on this device; CUB is not affected.',
      signin_title: 'Sign in',
      signin_button: 'Sign in',
      user_title: 'Account',
      signed_in_as: 'Signed in as',
      logout: 'Log out'
    },
    ru: {
      title: 'Аккаунт',
      descr: 'Вход через аккаунт Svtlv. Настройки, закладки и история остаются на этом устройстве; CUB не затрагивается.',
      signin_title: 'Авторизация',
      signin_button: 'Войти',
      user_title: 'Аккаунт',
      signed_in_as: 'Вы вошли как',
      logout: 'Выйти'
    }
  };

  // null while signed out; {id, name, email, picture} from /api/v1/session otherwise
  var user = null;

  function t(key) {
    var dict = strings[Lampa.Storage.get('language', 'ru')] || strings.en;

    return dict[key] || strings.en[key];
  }

  function fetchSession(onSuccess, onFail) {
    $.ajax({
      url: SESSION_URL,
      type: 'GET',
      dataType: 'json',
      cache: false,
      timeout: REQUEST_TIMEOUT,
      success: function (data) {
        if (data && typeof data.authenticated === 'boolean') onSuccess(data);
        else onFail();
      },
      error: function () {
        onFail();
      }
    });
  }

  function applySession(data) {
    user = data.authenticated && data.user ? data.user : null;
  }

  function row(name, value) {
    var item = $('<div class="settings-param selector" data-static="true"><div class="settings-param__name"></div></div>');

    item.find('.settings-param__name').text(name);

    // .text() keeps a server-provided value (email, name) from being parsed as HTML
    if (value) item.append($('<div class="settings-param__value"></div>').text(value));

    return item;
  }

  function title(text) {
    var item = $('<div class="settings-param-title"><span></span></div>');

    item.find('span').text(text);

    return item;
  }

  function renderSettings(body) {
    body.empty();
    body.append($('<div class="settings-param-text"></div>').text(t('descr')));

    if (user) {
      body.append(title(t('user_title')));
      body.append(row(t('signed_in_as'), user.email || user.name).addClass('svtlv-account__user'));
      body.append(row(t('logout')).addClass('svtlv-account__logout'));
    } else {
      body.append(title(t('signin_title')));
      body.append(row(t('signin_button')).addClass('settings-param--button svtlv-account__signin'));
    }

    // rows added after the component built its focus handlers; rebind so scroll follows focus
    Lampa.Params.listener.send('update_scroll');
  }

  function activate() {
    Lampa.SettingsApi.addComponent({
      component: COMPONENT,
      name: t('title'),
      // not 'account': Lampa removes the CUB folder when account_use is false, and an entry
      // anchored to a missing folder is silently dropped
      before: 'interface',
      icon: ICON
    });

    // replaces addComponent's bare <div></div> so the rows have a stable container
    Lampa.Template.add('settings_' + COMPONENT, '<div class="svtlv-account"></div>');

    Lampa.Settings.listener.follow('open', function (e) {
      if (e.name != COMPONENT) return;

      try {
        renderSettings(e.body.find('.svtlv-account'));
      } catch (err) {
        console.log('Account', 'settings render failed', err && err.message);
      }
    });
  }

  function heartbeat() {
    // 503 or a network error keeps the last known state: unavailable is not signed out
    fetchSession(applySession, function () {});
  }

  function init() {
    fetchSession(function (data) {
      applySession(data);
      activate();
      setInterval(heartbeat, HEARTBEAT_INTERVAL);
    }, function () {
      console.log('Account', 'session unavailable, add-on disabled');
    });
  }

  function safeInit() {
    try {
      init();
    } catch (err) {
      console.log('Account', 'init failed', err && err.message);
    }
  }

  function whenReady() {
    if (!window.Lampa || !window.Lampa.Listener) {
      setTimeout(whenReady, READY_POLL);
      return;
    }

    if (window.appready) safeInit();
    else {
      Lampa.Listener.follow('app', function (e) {
        if (e.type == 'ready') safeInit();
      });
    }
  }

  // file:// and app origins have no /api proxy; stay anonymous there
  if (!/^https?:$/.test(window.location.protocol)) return;

  if (window.svtlv_account_loaded) return;
  window.svtlv_account_loaded = true;

  whenReady();
})();
