(function () {
  'use strict';
  document.documentElement.classList.add('js');
  var toggle = document.querySelector('.nav-toggle');
  var nav = document.getElementById('primary-nav');
  function setMenu(open, returnFocus) {
    if (!toggle || !nav) return;
    toggle.setAttribute('aria-expanded', String(open));
    nav.classList.toggle('is-open', open);
    var label = toggle.querySelector('.sr-only');
    if (label) label.textContent = open ? '关闭导航' : '打开导航';
    if (!open && returnFocus) toggle.focus();
  }
  if (toggle && nav) {
    toggle.addEventListener('click', function () { setMenu(toggle.getAttribute('aria-expanded') !== 'true', false); });
    document.addEventListener('keydown', function (event) { if (event.key === 'Escape' && toggle.getAttribute('aria-expanded') === 'true') setMenu(false, true); });
    nav.addEventListener('click', function (event) { var link = event.target.closest('a'); if (link && toggle.getAttribute('aria-expanded') === 'true') setMenu(false, true); });
    window.addEventListener('resize', function () { if (window.matchMedia('(min-width: 861px)').matches) setMenu(false, false); });
  }
  document.querySelectorAll('[data-current-year]').forEach(function (node) { node.textContent = String(new Date().getFullYear()); });
}());
