// Theme bootstrap: runs in <head> (no defer) so the stored choice applies before
// first paint. Without a stored choice the page follows prefers-color-scheme.
(function () {
  'use strict';
  var KEY = 'entregaviva-theme';
  var root = document.documentElement;
  var media = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function stored() {
    try {
      var v = window.localStorage.getItem(KEY);
      return v === 'light' || v === 'dark' ? v : null;
    } catch (e) {
      return null;
    }
  }

  function effective() {
    return root.getAttribute('data-theme') || (media && media.matches ? 'dark' : 'light');
  }

  function sync() {
    var dark = effective() === 'dark';
    var buttons = document.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < buttons.length; i++) {
      buttons[i].setAttribute('aria-pressed', String(dark));
      buttons[i].setAttribute('aria-label', dark ? 'Usar tema claro' : 'Usar tema escuro');
      buttons[i].title = dark ? 'Tema claro' : 'Tema escuro';
    }
  }

  var initial = stored();
  if (initial) root.setAttribute('data-theme', initial);

  document.addEventListener('DOMContentLoaded', function () {
    sync();
    document.addEventListener('click', function (event) {
      var button = event.target.closest && event.target.closest('[data-theme-toggle]');
      if (!button) return;
      var next = effective() === 'dark' ? 'light' : 'dark';
      root.setAttribute('data-theme', next);
      try {
        window.localStorage.setItem(KEY, next);
      } catch (e) {
        /* storage unavailable: the choice lasts for this page only */
      }
      sync();
    });
    if (media && media.addEventListener) media.addEventListener('change', sync);
  });
})();
