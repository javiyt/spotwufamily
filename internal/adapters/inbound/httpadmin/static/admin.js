// Progressive enhancement only: every form works without this file. No network
// access beyond the admin server itself, no inline handlers.
(function () {
  'use strict';

  function isTyping(t) {
    return t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);
  }

  var toasts = document.getElementById('toasts');

  function toast(message, kind) {
    if (!toasts) return;
    var p = document.createElement('p');
    p.className = 'toast ' + kind;
    p.setAttribute('data-toast', '');
    p.textContent = message + ' ';
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'link';
    b.setAttribute('data-dismiss', '');
    b.setAttribute('aria-label', 'Dismiss');
    b.textContent = '×';
    p.appendChild(b);
    toasts.appendChild(p);
    if (kind === 'ok') setTimeout(function () { p.remove(); }, 6000);
  }

  // "/" focuses search; Esc cancels or dismisses; Ctrl/Cmd+Enter saves.
  document.addEventListener('keydown', function (e) {
    if (e.key === '/' && !e.ctrlKey && !e.metaKey && !e.altKey && !isTyping(e.target)) {
      var box = document.querySelector('[data-hotkey="/"]');
      if (box) { e.preventDefault(); box.focus(); }
      return;
    }
    if (e.key === 'Escape') {
      var open = document.querySelector('details[open]');
      if (open && open.contains(e.target)) { open.removeAttribute('open'); return; }
      var t = toasts && toasts.querySelector('[data-toast]');
      if (t) { t.remove(); return; }
      var cancel = document.querySelector('[data-cancel]');
      if (cancel && document.querySelector('form[data-editor], form [name="intent"]')) { window.location.href = cancel.href; }
      return;
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      var form = e.target && e.target.form;
      if (!form) return;
      var save = form.querySelector('button[name="intent"][value="save"]');
      if (save) { e.preventDefault(); save.click(); }
    }
  });

  document.addEventListener('click', function (e) {
    var d = e.target.closest && e.target.closest('[data-dismiss]');
    if (d) d.closest('[data-toast]').remove();
  });

  // Offline state: saving is disabled while the browser reports no network.
  var banner = document.getElementById('offline');
  function syncOnline() {
    var off = navigator.onLine === false;
    if (banner) banner.hidden = !off;
    document.querySelectorAll('form[method="post"] button[type="submit"]').forEach(function (b) {
      if (off) { b.setAttribute('data-was-enabled', b.disabled ? '0' : '1'); b.disabled = true; }
      else if (b.getAttribute('data-was-enabled') === '1') { b.disabled = false; }
    });
  }
  window.addEventListener('online', syncOnline);
  window.addEventListener('offline', syncOnline);
  syncOnline();

  // Submit through fetch so the toast reports what the server actually did.
  // Success is a JSON confirmation; any other answer is the server's own page
  // (validation errors, conflicts, previews) and replaces this one.
  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (!form.matches || !form.matches('form[method="post"]') || form.action.indexOf('/logout') !== -1) return;
    var msg = form.getAttribute('data-confirm');
    if (msg && !window.confirm(msg)) { e.preventDefault(); return; }
    if (!window.fetch || !window.FormData) return;
    e.preventDefault();
    var data = new URLSearchParams(new FormData(form));
    if (e.submitter && e.submitter.name) data.set(e.submitter.name, e.submitter.value);
    fetch(form.action, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Admin-Fetch': '1' },
      body: data,
      credentials: 'same-origin'
    }).then(function (res) {
      var type = res.headers.get('Content-Type') || '';
      if (res.ok && type.indexOf('application/json') !== -1) {
        return res.json().then(function (body) {
          toast(body.message || 'Saved.', 'ok');
          setTimeout(function () { window.location.href = body.redirect || window.location.href; }, 700);
        });
      }
      return res.text().then(function (html) {
        document.open(); document.write(html); document.close();
      });
    }).catch(function () {
      toast('The admin server could not be reached. Nothing was saved; your edits are still in the form.', 'err');
    });
  });
})();
