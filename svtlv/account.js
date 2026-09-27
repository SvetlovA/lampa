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
      account_settings: 'Account settings'
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
      account_settings: 'Настройки аккаунта'
    }
  };

  // null while signed out; {id, name, email, picture} from /api/v1/session otherwise
  var user = null;
  // the header icon, created once the add-on activates
  var headIcon = null;

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
    var before = user ? user.id : '';

    user = data.authenticated && data.user ? data.user : null;

    if ((user ? user.id : '') != before) updateHeadIcon();
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

  // until the sign-in and sign-out flows exist they start from the Account settings page
  function signIn() {
    openSettings();
  }

  function logOut() {
    openSettings();
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
      onSelect: signIn
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

  // CUB's own profile list, with "Sign in to Account" added on its way to the screen
  function cubProfiles() {
    var extra = function (e) {
      Lampa.Select.listener.remove('preshow', extra);

      // the list loads asynchronously and may fail; only CUB's own Select gets the item
      if (e.active.title != Lampa.Lang.translate('account_profiles')) return;

      e.active.items.push(action('signin_to_account', signIn));
    };

    Lampa.Select.listener.follow('preshow', extra);
    Lampa.Account.Profile.select(backToHead);
  }

  function openMenu() {
    var cub = cubSignedIn();
    var items;

    if (!user && !cub) return signInChooser();
    if (!user) return cubProfiles();

    items = [profileRow()];

    if (cub) {
      items.push(action('switch_cub_profile', function () {
        Lampa.Account.Profile.select(backToHead);
      }));
    } else if (cubEnabled()) items.push(action('signin_to_cub', cubSignIn));

    items.push(action('account_settings', openSettings));
    items.push(action('logout', logOut));

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

    createHeadIcon();
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
