(function () {
  var t = null;
  try { t = localStorage.getItem('httpcatch.theme'); } catch (e) {}
  if (t !== 'dark' && t !== 'light') t = 'light';
  document.documentElement.setAttribute('data-theme', t);
})();
