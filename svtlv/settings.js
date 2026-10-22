/*
 * Svtlv settings add-on. ES5, public Lampa APIs only.
 * Exposes existing preferences without depending on the Account API.
 */
(function () {
  'use strict';

  if (!/^https?:$/.test(window.location.protocol)) return;
  if (window.svtlv_settings_loaded) return;
  window.svtlv_settings_loaded = true;

  var activated = false;
  var adultItem;
  var strings = {
    en: {
      subtitle_title: 'Subtitle appearance',
      subtitle_descr: 'Applies to subtitles displayed by the built-in player.',
      subtitle_size: 'Subtitle size',
      subtitle_outline: 'Subtitle outline',
      subtitle_background: 'Subtitle background',
      small: 'Small', normal: 'Normal', large: 'Large',
      segments_title: 'Skip segments',
      segments_descr: 'Applies when the built-in player has segment information.',
      intro: 'Intro and other segments', ads: 'Advertisement segments',
      automatic: 'Automatic', manual: 'Manual', off: 'Do not skip',
      adult: 'Allow adult content',
      adult_descr: 'Remember approval for adult content. Turn off to require confirmation again.',
      adult_confirm: 'You must be 18 or older to enable adult content.'
    },
    ru: {
      subtitle_title: 'Оформление субтитров',
      subtitle_descr: 'Для субтитров, отображаемых встроенным плеером.',
      subtitle_size: 'Размер субтитров',
      subtitle_outline: 'Окантовка субтитров',
      subtitle_background: 'Подложка субтитров',
      small: 'Маленький', normal: 'Обычный', large: 'Большой',
      segments_title: 'Пропуск сегментов',
      segments_descr: 'Работает, если встроенному плееру доступны данные о сегментах.',
      intro: 'Интро и другие сегменты', ads: 'Рекламные вставки',
      automatic: 'Автоматически', manual: 'Вручную', off: 'Не пропускать',
      adult: 'Разрешить взрослый контент',
      adult_descr: 'Запомнить разрешение на взрослый контент. Выключите, чтобы снова запрашивать подтверждение.',
      adult_confirm: 'Для доступа к взрослому контенту вам должно быть 18 лет или больше.'
    }
  };

  function t(key) {
    var language = Lampa.Storage.get('language', 'ru');
    return (strings[language] || strings.en)[key];
  }

  function available() {
    var flags = window.lampa_settings || {};
    return !flags.iptv && !flags.demo && !flags.white_use;
  }

  function childProfile() {
    return Lampa.Account && Lampa.Account.Permit && Lampa.Account.Permit.child;
  }

  function add(component, param, field, options) {
    var params = Lampa.SettingsApi.getParam(component) || [];
    for (var i = 0; i < params.length; i++) {
      if (param.name && params[i].param.name == param.name) return;
    }
    var data = {
      component: component,
      param: param,
      field: field,
      onRender: function (item) {
        // onRender runs before Lampa appends the item, so remove() would reinsert it.
        if (!available()) item.addClass('hide');
        else if (options && options.onRender) options.onRender(item);
      }
    };
    if (options && options.onChange) data.onChange = options.onChange;
    Lampa.SettingsApi.addParam(data);
  }

  function drawAdult() {
    if (adultItem) adultItem.find('.settings-param__value').text(Lampa.Lang.translate(
      Lampa.Storage.field('adult_content_view') ? 'settings_param_yes' : 'settings_param_no'
    ));
  }

  function changeAdult() {
    if (!available() || childProfile()) return;
    if (Lampa.Storage.field('adult_content_view')) {
      Lampa.Storage.set('adult_content_view', false);
      drawAdult();
      return;
    }
    var controller = Lampa.Controller.enabled().name;
    function close() {
      Lampa.Modal.close();
      Lampa.Controller.toggle(controller);
    }
    Lampa.Modal.open({
      title: Lampa.Lang.translate('adult_content_title'),
      size: 'small',
      scroll: {nopadding: true},
      html: $('<div class="about"></div>').text(t('adult_confirm')),
      buttons: [{
        name: Lampa.Lang.translate('adult_content_confirm'),
        onSelect: function () {
          if (available() && !childProfile()) Lampa.Storage.set('adult_content_view', true);
          drawAdult();
          close();
        }
      }, {
        name: Lampa.Lang.translate('adult_content_deny'),
        onSelect: close
      }],
      onBack: close
    });
  }

  function revealMirror(event) {
    var flags = window.lampa_settings || {};
    if (event.name != 'account' || !available() || flags.account_use === false) return;
    var item = event.body.find('[data-name="cub_domain"]');
    if (!item.length) return;
    var values = {};
    var mirrors = Lampa.Manifest.cub_mirrors;
    for (var i = 0; i < mirrors.length; i++) values[mirrors[i]] = mirrors[i];
    Lampa.Params.select('cub_domain', values, Lampa.Manifest.cub_domain);
    item.removeClass('hide').data('onChange', function () {
      // Match the existing developer selector's mirror-failover update.
      Lampa.Storage.set('cub_alive', Lampa.Utils.protocol() + Lampa.Manifest.cub_domain, true);
    });
    Lampa.Params.update(item);
    Lampa.Params.listener.send('update_scroll');
  }

  function activate() {
    if (activated) return;
    activated = true;
    if (!available()) return;
    add('player', {type: 'title'}, {name: t('subtitle_title'), description: t('subtitle_descr')});
    add('player', {
      name: 'subtitles_size', type: 'select',
      values: {small: t('small'), normal: t('normal'), large: t('large')}, 'default': 'normal'
    }, {name: t('subtitle_size')});
    add('player', {name: 'subtitles_stroke', type: 'trigger', 'default': true}, {name: t('subtitle_outline')});
    add('player', {name: 'subtitles_backdrop', type: 'trigger', 'default': false}, {name: t('subtitle_background')});
    add('player', {type: 'title'}, {name: t('segments_title'), description: t('segments_descr')});
    var segmentValues = {auto: t('automatic'), user: t('manual'), none: t('off')};
    add('player', {
      name: 'player_segments_skip', type: 'select', values: segmentValues, 'default': 'auto'
    }, {name: t('intro')});
    add('player', {
      name: 'player_segments_ad', type: 'select', values: segmentValues, 'default': 'auto'
    }, {name: t('ads')});
    add('more', {name: 'adult_content_view', type: 'button'}, {
      name: t('adult'), description: t('adult_descr')
    }, {
      onRender: function (item) {
        if (childProfile()) { item.addClass('hide'); return; }
        adultItem = item;
        item.find('.settings-param__name').after('<div class="settings-param__value"></div>');
        drawAdult();
      },
      onChange: changeAdult
    });
    Lampa.Storage.listener.follow('change', function (event) {
      if (event.name == 'adult_content_view') drawAdult();
    });
    Lampa.Settings.listener.follow('open', revealMirror);
  }

  function safeActivate() {
    try { activate(); }
    catch (err) { console.log('Svtlv settings', 'init failed', err && err.message); }
  }

  function whenReady() {
    if (!window.Lampa || !Lampa.SettingsApi || !Lampa.Listener) {
      setTimeout(whenReady, 200);
      return;
    }
    if (window.appready) safeActivate();
    else Lampa.Listener.follow('app', function (event) {
      if (event.type == 'ready') safeActivate();
    });
  }

  whenReady();
})();
