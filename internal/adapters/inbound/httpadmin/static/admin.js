// Press "/" to jump to the search box. No network access, no inline handlers.
document.addEventListener('keydown', function (e) {
  if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
  var t = e.target;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
  var box = document.querySelector('[data-hotkey="/"]');
  if (box) { e.preventDefault(); box.focus(); }
});
