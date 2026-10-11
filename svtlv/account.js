/*
 * Svtlv Account add-on (Plan 2, design §10.5).
 *
 * Identity only: reads GET /api/v1/session, runs sign-in and sign-out, and never
 * calls the user-data API.
 * ES5 only (old TV browsers), public window.Lampa APIs only, never app.min.js
 * internals. Any failure leaves anonymous Lampa and CUB working as before.
 */
(function () {
  'use strict';

  var SESSION_URL = '/api/v1/session';
  var LOGIN_URL = '/api/v1/auth/login';
  var DEVICE_START_URL = '/api/v1/auth/device/start';
  var DEVICE_POLL_URL = '/api/v1/auth/device/poll';
  var LOGOUT_URL = '/api/v1/auth/logout';
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
      logout: 'Log out',
      menu_title: 'Profile',
      signin_menu: 'Sign in',
      signin_account: 'Account',
      signin_account_descr: 'Svtlv account',
      signin_cub: 'CUB',
      signin_cub_descr: 'Bookmarks and history sync',
      signin_to_account: 'Sign in to Account',
      signin_to_cub: 'Sign in to CUB',
      switch_cub_profile: 'Switch CUB profile',
      account_settings: 'Account settings',
      cancel: 'Cancel',
      signin_failed: 'Sign-in failed',
      signin_unavailable: 'Sign-in is unavailable, try again later',
      device_title: 'Sign in to Account',
      device_qr: 'Scan the QR code with your phone',
      device_text: 'Or open this address on your phone or computer and enter the code:',
      device_waiting: 'Waiting for confirmation…',
      device_retrying: 'No connection to the server, retrying…',
      device_expires: 'The code expires in',
      device_denied: 'Sign-in was denied',
      device_expired: 'The code has expired, start again',
      logout_confirm: 'Log out of Account?',
      logout_descr_browser: 'Also signs out of Svtlv in this browser; a TV approved here may sign out too',
      logout_descr_tv: 'Signs out on this TV; your phone may still be signed in to Svtlv',
      logout_failed: 'Could not log out, try again',
      logout_sso_failed: 'Signed out here, but could not sign out of Svtlv in this browser',
      logged_out: 'Signed out of Account'
    },
    ru: {
      title: 'Аккаунт',
      descr: 'Вход через аккаунт Svtlv. Настройки, закладки и история остаются на этом устройстве; CUB не затрагивается.',
      signin_title: 'Авторизация',
      signin_button: 'Войти',
      user_title: 'Аккаунт',
      signed_in_as: 'Вы вошли как',
      logout: 'Выйти',
      menu_title: 'Профиль',
      signin_menu: 'Войти',
      signin_account: 'Аккаунт',
      signin_account_descr: 'Аккаунт Svtlv',
      signin_cub: 'CUB',
      signin_cub_descr: 'Синхронизация закладок и истории',
      signin_to_account: 'Войти в Аккаунт',
      signin_to_cub: 'Войти в CUB',
      switch_cub_profile: 'Сменить профиль CUB',
      account_settings: 'Настройки аккаунта',
      cancel: 'Отмена',
      signin_failed: 'Не удалось войти',
      signin_unavailable: 'Вход сейчас недоступен, попробуйте позже',
      device_title: 'Вход в Аккаунт',
      device_qr: 'Отсканируйте QR-код телефоном',
      device_text: 'Или откройте этот адрес на телефоне или компьютере и введите код:',
      device_waiting: 'Ожидаем подтверждения…',
      device_retrying: 'Нет связи с сервером, повторяем…',
      device_expires: 'Код действует ещё',
      device_denied: 'Вход отклонён',
      device_expired: 'Срок действия кода истёк, начните заново',
      logout_confirm: 'Выйти из Аккаунта?',
      logout_descr_browser: 'Также выйдет из Svtlv в этом браузере; ТВ, подключённый через него, тоже может выйти',
      logout_descr_tv: 'Выход на этом ТВ; на телефоне вход в Svtlv может сохраниться',
      logout_failed: 'Не удалось выйти, попробуйте ещё раз',
      logout_sso_failed: 'Здесь вы вышли, но из Svtlv в этом браузере выйти не удалось',
      logged_out: 'Вы вышли из Аккаунта'
    }
  };

  // null while signed out; {id, name, email, picture} from /api/v1/session otherwise
  var user = null;
  // the header icon, created once the add-on activates
  var headIcon = null;
  // the Account settings page container while it is rendered
  var settingsBody = null;
  // the TV device login in progress, if any
  var device = null;
  // 'ok' or 'failed' from the #svtlv-login fragment of a redirect login
  var loginResult = '';
  // the pending preshow hook of cubProfiles; CUB may never show its list (request failed,
  // PIN cancelled), so a hook can outlive its menu: the head menu and every settings page
  // opening drop it
  var profilesHook = null;
  // GET /session requests still in flight; each may answer with a renewed session cookie
  var sessionPending = 0;
  // callbacks waiting for sessionPending to reach 0
  var sessionIdle = [];
  // true from the logout confirmation until POST /auth/logout answers
  var loggingOut = false;

  function t(key) {
    var dict = strings[Lampa.Storage.get('language', 'ru')] || strings.en;

    return dict[key] || strings.en[key];
  }

  function fetchSession(onSuccess, onFail) {
    sessionPending++;

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
      },
      complete: function () {
        var waiting;

        sessionPending--;

        if (sessionPending) return;

        waiting = sessionIdle;
        sessionIdle = [];
        waiting.forEach(function (call) {
          call();
        });
      }
    });
  }

  // runs call once no GET /session is in flight, so a late renewed cookie cannot land after it
  function whenSessionIdle(call) {
    if (sessionPending) sessionIdle.push(call);
    else call();
  }

  // POST with the CSRF header; done(status, body) gets status 0 on a network error or timeout
  function post(url, done) {
    return $.ajax({
      url: url,
      type: 'POST',
      dataType: 'text',
      cache: false,
      timeout: REQUEST_TIMEOUT,
      headers: {
        'X-Lampa-Csrf': '1'
      },
      complete: function (xhr) {
        var body = null;

        try {
          body = xhr.responseText ? JSON.parse(xhr.responseText) : null;
        } catch (err) {
          body = null;
        }

        done(xhr.status || 0, body);
      }
    });
  }

  // Noty, Select and settings rows render HTML
  function esc(v) {
    return $('<i>').text(v || '').html();
  }

  function applySession(data) {
    var before = user ? user.id : '';

    user = data.authenticated && data.user ? data.user : null;

    if ((user ? user.id : '') != before) {
      updateHeadIcon();
      refreshSettings();
    }
  }

  function signedInNoty() {
    Lampa.Noty.show(t('signed_in_as') + ' ' + esc(user.email || user.name));
  }

  // read at call time: a build variant may turn CUB off before or after the add-on loads
  function cubEnabled() {
    return !!window.lampa_settings.account_use;
  }

  // CUB state is read only through Permit; its access already implies account_use
  function cubSignedIn() {
    return cubEnabled() && !!Lampa.Account.Permit.access;
  }

  function initials(u) {
    var words = $.trim(u.name || u.email || '').split(/\s+/);
    var letters = words[0].charAt(0) + (words.length > 1 ? words[1].charAt(0) : '');

    return letters.toUpperCase();
  }

  function plainIcon() {
    return $(Lampa.Template.string('icon_profile'));
  }

  // src is set only through img.src, never through HTML, so a URL cannot inject markup
  function imageIcon(src, fallback) {
    var img = document.createElement('img');

    img.onerror = function () {
      img.onerror = null;
      $(img).replaceWith(fallback());
    };
    img.src = src;

    return $(img);
  }

  function initialsIcon(u) {
    var letters = initials(u);

    if (!letters) return plainIcon();

    return $('<div class="svtlv-account__initials"></div>').text(letters);
  }

  function userAvatar(u) {
    var fallback = function () {
      return initialsIcon(u);
    };

    if (u.picture && /^https:\/\//i.test(u.picture)) return imageIcon(u.picture, fallback);

    return fallback();
  }

  // Account avatar, else CUB's profile image, else the plain profile icon
  function avatar() {
    if (user) return userAvatar(user);

    var cub = cubSignedIn() ? Lampa.Account.Profile.icon() : '';

    if (cub) return imageIcon(cub, plainIcon);

    return plainIcon();
  }

  function updateHeadIcon() {
    if (headIcon) headIcon.empty().append(avatar());
  }

  function backToHead() {
    Lampa.Controller.toggle('head');
  }

  function openSettings() {
    Lampa.Controller.toggle('settings');
    Lampa.Settings.create(COMPONENT);
  }

  // returns focus to the controller active now; the settings rows start flows from there
  function restoreHere() {
    var name = Lampa.Controller.enabled().name;

    return function () {
      Lampa.Controller.toggle(name);
    };
  }

  // re-render the Account page only while it has focus; otherwise it is current when next opened
  function refreshSettings() {
    if (!settingsBody || !$('body').hasClass('settings--open')) return;
    if (!$.contains(document.documentElement, settingsBody[0])) return;
    if (Lampa.Controller.enabled().name != 'settings_component') return;

    Lampa.Settings.update();
  }

  // the server accepts only a local path without a fragment and falls back to / otherwise
  function redirectSignIn() {
    window.location.href = LOGIN_URL + '?return=' + encodeURIComponent(window.location.pathname + window.location.search);
  }

  function codeGroups(code) {
    var parts = String(code).split('-');

    if (parts.length > 1) return parts;

    return String(code).match(/.{1,4}/g) || [String(code)];
  }

  function pad(n) {
    return n < 10 ? '0' + n : '' + n;
  }

  // screen 3A; every server string goes in through .text()
  function deviceView(start) {
    var html = $('<div class="account-modal-split svtlv-account-device">' +
      '<div class="account-modal-split__qr"><div class="account-modal-split__qr-code"></div>' +
      '<div class="account-modal-split__qr-text"></div></div>' +
      '<div class="account-modal-split__info"><div class="account-modal-split__title"></div>' +
      '<div class="account-modal-split__text"></div>' +
      '<div class="svtlv-account-device__uri"></div><div class="svtlv-account-device__code"></div>' +
      '<div class="svtlv-account-device__status"></div><div class="svtlv-account-device__timer"></div>' +
      '<div class="simple-button simple-button--inline selector"></div>' +
      '</div></div>');
    var code = html.find('.svtlv-account-device__code');

    html.addClass('layer--' + (Lampa.Platform.mouse() ? 'wheight' : 'height'));
    html.find('.account-modal-split__qr-text').text(t('device_qr'));
    html.find('.account-modal-split__title').text(t('device_title'));
    html.find('.account-modal-split__text').text(t('device_text'));
    html.find('.svtlv-account-device__uri').text(start.verification_uri);
    html.find('.svtlv-account-device__status').text(t('device_waiting'));
    html.find('.simple-button').text(t('cancel'));

    codeGroups(start.user_code).forEach(function (group) {
      code.append($('<span></span>').text(group));
    });

    Lampa.Utils.qrcode(start.verification_uri_complete, html.find('.account-modal-split__qr-code'), function () {
      html.find('.account-modal-split__qr').remove();
    });

    return html;
  }

  // stops the timers and the request in flight; a late answer sees flow.done and is dropped
  function finishDevice(flow, message) {
    if (flow.done) return;

    flow.done = true;
    clearTimeout(flow.poll);
    clearInterval(flow.countdown);

    if (flow.xhr) flow.xhr.abort();
    if (device === flow) device = null;
    if (flow.modal) Lampa.Modal.close();

    flow.restore();

    if (message) Lampa.Noty.show(message);
  }

  function tick(flow) {
    var left = Math.ceil((flow.expiresAt - new Date().getTime()) / 1000);

    if (left <= 0) return finishDevice(flow, t('device_expired'));

    flow.timer.text(t('device_expires') + ' ' + Math.floor(left / 60) + ':' + pad(left % 60));
  }

  // slow_down arrives as a longer interval from the server; otherwise keep the last one
  function schedulePoll(flow, seconds) {
    if (seconds > 0) flow.interval = seconds;

    flow.poll = setTimeout(function () {
      pollDevice(flow);
    }, flow.interval * 1000);
  }

  function pollDevice(flow) {
    flow.xhr = post(DEVICE_POLL_URL, function (status, body) {
      if (flow.done) return;

      if (status == 200 && body && body.authenticated === true && body.user) {
        finishDevice(flow);
        applySession(body);
        signedInNoty();
      } else if (status == 202) {
        flow.status.text(t('device_waiting'));
        schedulePoll(flow, body && body.interval);
      } else if (status == 403) finishDevice(flow, t('device_denied'));
      else if (status == 410 || status == 400) finishDevice(flow, t('device_expired'));
      else if (status == 0 || status >= 502) {
        // Keycloak or the API is briefly unavailable: no answer yet, keep polling until the code expires
        flow.status.text(t('device_retrying'));
        schedulePoll(flow);
      } else finishDevice(flow, t('signin_failed'));
    });
  }

  function openDevice(flow, start) {
    var html = deviceView(start);

    flow.modal = true;
    flow.interval = start.interval;
    flow.expiresAt = new Date().getTime() + start.expires_in * 1000;
    flow.status = html.find('.svtlv-account-device__status');
    flow.timer = html.find('.svtlv-account-device__timer');

    Lampa.Modal.open({
      title: '',
      html: html,
      size: 'full',
      scroll: {
        nopadding: true
      },
      // the only selector is Cancel
      onSelect: function () {
        finishDevice(flow);
      },
      onBack: function () {
        finishDevice(flow);
      }
    });

    tick(flow);
    flow.countdown = setInterval(function () {
      tick(flow);
    }, 1000);
    schedulePoll(flow);
  }

  function deviceSignIn(restore) {
    var flow;

    if (device) return;

    flow = device = {
      restore: restore
    };

    flow.xhr = post(DEVICE_START_URL, function (status, body) {
      if (flow.done) return;

      if (status == 200 && body && body.user_code && body.verification_uri && body.expires_in > 0 && body.interval > 0) openDevice(flow, body);
      else finishDevice(flow, t(status == 0 || status >= 502 ? 'signin_unavailable' : 'signin_failed'));
    });
  }

  // TVs get the device grant (screen 3A); phones and computers go through Keycloak's page (3B)
  function signIn(restore) {
    if (Lampa.Platform.tv()) deviceSignIn(restore);
    else redirectSignIn();
  }

  function signInFromHead() {
    signIn(backToHead);
  }

  // 204 also when already signed out; anything else keeps the current state. a /session
  // response of this tab arriving after the logout would show the user signed in again, so the
  // logout waits for those in flight and the heartbeat pauses until it answers (the server
  // refuses the renewed cookie of any tab by its lampa_logout mark)
  function doLogOut() {
    if (loggingOut) return;

    loggingOut = true;

    whenSessionIdle(function () {
      post(LOGOUT_URL + (Lampa.Platform.tv() ? '' : '?sso=1'), logOutDone);
    });
  }

  function logOutDone(status, body) {
    loggingOut = false;

    if (status == 204 || status == 200) {
      applySession({
        authenticated: false
      });
      if (status == 200 && body && body.logout_url) window.location.href = body.logout_url;
      else Lampa.Noty.show(t(status == 200 && (!body || !body.sso_logged_out) ? 'logout_sso_failed' : 'logged_out'));
    } else Lampa.Noty.show(t('logout_failed'));
  }

  // screen 5
  function logOut(restore) {
    Lampa.Select.show({
      title: t('logout_confirm'),
      items: [{
        title: t('logout'),
        subtitle: t(Lampa.Platform.tv() ? 'logout_descr_tv' : 'logout_descr_browser'),
        logout: true
      }, {
        title: t('cancel')
      }],
      onSelect: function (item) {
        restore();

        if (item.logout) doLogOut();
      },
      onBack: restore
    });
  }

  // the modal returns focus to the controller active when it opened, so leave the closed Select first
  function cubSignIn() {
    backToHead();
    Lampa.Account.Modal.account();
  }

  // built from DOM nodes, not a template: Template.get would parse name and email as HTML
  // and expand "$&"-style sequences in them as replacement patterns
  function profileRow() {
    var name = user.name || user.email || '';
    var email = user.email && user.email != name ? user.email : '';
    var item = $('<div class="selectbox-item selectbox-item--icon selector svtlv-account__profile">' +
      '<div class="selectbox-item__icon"></div><div>' +
      '<div class="selectbox-item__title"></div><div class="selectbox-item__subtitle"></div>' +
      '</div></div>');

    item.find('.selectbox-item__icon').append(userAvatar(user));
    item.find('.selectbox-item__title').text(name);
    item.find('.selectbox-item__subtitle').text(email);

    // Select drops the subtitle node when the item has no subtitle; it never renders these
    return {
      title: $('<i>').text(name).html(),
      subtitle: $('<i>').text(email).html(),
      html: item,
      noenter: true
    };
  }

  function action(key, run) {
    return {
      title: t(key),
      onSelect: run
    };
  }

  function signInChooser() {
    var items = [{
      title: t('signin_account'),
      subtitle: t('signin_account_descr'),
      onSelect: signInFromHead
    }];

    if (cubEnabled()) {
      items.push({
        title: t('signin_cub'),
        subtitle: t('signin_cub_descr'),
        onSelect: cubSignIn
      });
    }

    Lampa.Select.show({
      title: t('signin_menu'),
      items: items,
      onBack: backToHead
    });
  }

  function unhookProfiles() {
    if (profilesHook) Lampa.Select.listener.remove('preshow', profilesHook);

    profilesHook = null;
  }

  // CUB's own profile list, with "Sign in to Account" added on its way to the screen
  function cubProfiles() {
    unhookProfiles();

    profilesHook = function (e) {
      // the list loads asynchronously, so another Select may show first: that one is skipped
      // and the hook waits for CUB's own list
      if (e.active.title != Lampa.Lang.translate('account_profiles')) return;

      unhookProfiles();

      // only while Account is still signed out
      if (!user) e.active.items.push(action('signin_to_account', signInFromHead));
    };

    Lampa.Select.listener.follow('preshow', profilesHook);
    Lampa.Account.Profile.select(backToHead);
  }

  function openMenu() {
    var cub = cubSignedIn();
    var items;

    // a hook left by a CUB list that never showed must not reach one opened from here
    unhookProfiles();

    if (!user && !cub) return signInChooser();
    if (!user) return cubProfiles();

    items = [profileRow()];

    if (cub) {
      items.push(action('switch_cub_profile', function () {
        Lampa.Account.Profile.select(backToHead);
      }));
    } else if (cubEnabled()) items.push(action('signin_to_cub', cubSignIn));

    items.push(action('account_settings', openSettings));
    items.push(action('logout', function () {
      logOut(backToHead);
    }));

    Lampa.Select.show({
      title: t('menu_title'),
      items: items,
      onBack: backToHead
    });
  }

  function createHeadIcon() {
    var head = Lampa.Head.render();
    var slot = head.find('.full--screen');

    headIcon = $('<div class="head__action selector open--account"></div>');
    headIcon.on('hover:enter', function () {
      try {
        openMenu();
      } catch (err) {
        console.log('Account', 'menu failed', err && err.message);
      }
    });

    // CUB's profile icon slot; account.css hides CUB's own icon once the body class is set
    if (slot.length) slot.before(headIcon);
    else head.find('.head__actions').append(headIcon);

    $('body').addClass('svtlv-account--active');

    updateHeadIcon();

    Lampa.Storage.listener.follow('change', function (e) {
      if (e.name == 'account') updateHeadIcon();
    });
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
    settingsBody = body;
    body.empty();
    body.append($('<div class="settings-param-text"></div>').text(t('descr')));

    if (user) {
      body.append(title(t('user_title')));
      body.append(row(t('signed_in_as'), user.email || user.name).addClass('svtlv-account__user'));
      body.append(row(t('logout')).addClass('svtlv-account__logout').on('hover:enter', function () {
        logOut(restoreHere());
      }));
    } else {
      body.append(title(t('signin_title')));
      body.append(row(t('signin_button')).addClass('settings-param--button svtlv-account__signin').on('hover:enter', function () {
        signIn(restoreHere());
      }));
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
      // CUB's settings folder opens the same list; a leftover head hook must not reach it
      unhookProfiles();

      if (e.name != COMPONENT) return;

      try {
        renderSettings(e.body.find('.svtlv-account'));
      } catch (err) {
        console.log('Account', 'settings render failed', err && err.message);
      }
    });

    createHeadIcon();
  }

  function showLoginResult() {
    if (loginResult == 'ok' && user) signedInNoty();
    else if (loginResult) Lampa.Noty.show(t('signin_failed'));

    loginResult = '';
  }

  function heartbeat() {
    // an answer landing after a logout in flight would show the user signed in again (see doLogOut)
    if (loggingOut) return;

    // 503 or a network error keeps the last known state: unavailable is not signed out
    fetchSession(applySession, function () {});
  }

  function init() {
    fetchSession(function (data) {
      applySession(data);
      activate();
      showLoginResult();
      setInterval(heartbeat, HEARTBEAT_INTERVAL);
    }, function () {
      if (loginResult) Lampa.Noty.show(t('signin_failed'));

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

  // the callback redirect ends in #svtlv-login=ok|failed; drop it so a reload does not repeat the Noty
  function takeLoginResult() {
    var match = /^#svtlv-login=(ok|failed)$/.exec(window.location.hash);

    if (!match) return '';

    if (window.history && window.history.replaceState) {
      window.history.replaceState(null, '', window.location.pathname + window.location.search);
    }

    return match[1];
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

  // read at load, before Lampa's first pushState rewrites the address and drops the fragment
  loginResult = takeLoginResult();

  whenReady();
})();
