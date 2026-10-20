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

// Profile review: remember the reviewer's name for this browser, and let a
// suggested source fill the evidence fields of the form it sits in.
(function () {
  'use strict';
  var key = 'swf-admin-reviewer';
  var saved = '';
  try { saved = window.localStorage.getItem(key) || ''; } catch (e) { saved = ''; }
  document.querySelectorAll('input[data-reviewer]').forEach(function (input) {
    if (!input.value && saved) input.value = saved;
    input.addEventListener('change', function () {
      try { window.localStorage.setItem(key, input.value.trim()); } catch (e) { /* storage may be blocked */ }
    });
  });
  document.addEventListener('click', function (e) {
    var b = e.target.closest && e.target.closest('[data-fill-url]');
    if (!b) return;
    var form = b.closest('form');
    if (!form) return;
    var set = function (name, value) { var el = form.elements[name]; if (el) el.value = value; };
    set('evidence_type', b.getAttribute('data-fill-type'));
    set('evidence_url', b.getAttribute('data-fill-url'));
    set('evidence_rationale', b.getAttribute('data-fill-rationale'));
  });
})();

// Job progress: while a job is active, poll its JSON every 2 s and update the
// page in place; reload once when it finishes so the server renders the final
// state. Pages without a job section are left alone. Without JavaScript the
// page refreshes itself through a <noscript> meta refresh instead.
(function () {
  'use strict';
  var root = document.querySelector('[data-job-id]');
  if (!root || !window.fetch) return;
  var id = root.getAttribute('data-job-id');
  var active = { queued: true, running: true, cancelling: true };
  if (!id || !active[root.getAttribute('data-job-status')]) return;

  var labels = {
    queued: 'Queued', running: 'Running', cancelling: 'Cancelling', succeeded: 'Succeeded',
    partial: 'Partially completed', failed: 'Failed', cancelled: 'Cancelled', interrupted: 'Interrupted'
  };
  var url = '/api/v1/jobs/' + encodeURIComponent(id);
  var banner = document.getElementById('offline');
  var reloading = false;

  function setField(name, value) {
    root.querySelectorAll('[data-job-field="' + name + '"]').forEach(function (el) { el.textContent = value; });
  }

  function update(job) {
    setField('status', labels[job.status] || String(job.status));
    setField('step', job.step ? String(job.step) : '—');
    setField('done', String(job.done || 0));
    setField('total', String(job.total || 0));
    root.setAttribute('data-job-status', String(job.status));
    var bar = root.querySelector('progress[data-job-progress]');
    if (bar) {
      if (job.total > 0) { bar.max = job.total; bar.value = job.done || 0; }
      else { bar.removeAttribute('value'); bar.removeAttribute('max'); }
    }
    var body = root.querySelector('[data-job-counts]');
    if (body && job.counts && typeof job.counts === 'object') {
      var keys = Object.keys(job.counts).sort();
      while (body.firstChild) body.removeChild(body.firstChild);
      if (keys.length === 0) {
        var empty = document.createElement('tr');
        var td = document.createElement('td');
        td.colSpan = 2;
        td.textContent = 'No counts reported.';
        empty.appendChild(td);
        body.appendChild(empty);
      }
      keys.forEach(function (k) {
        var tr = document.createElement('tr');
        var th = document.createElement('th');
        th.scope = 'row';
        th.textContent = k;
        var v = document.createElement('td');
        v.textContent = String(job.counts[k]);
        tr.appendChild(th);
        tr.appendChild(v);
        body.appendChild(tr);
      });
    }
  }

  function reloadOnce() {
    if (reloading) return;
    reloading = true;
    window.location.reload();
  }

  function poll() {
    fetch(url, { credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' } })
      .then(function (res) {
        // Signed out or gone: let the server render what is true now.
        if (res.status === 401 || res.status === 404) { reloadOnce(); return null; }
        if (!res.ok) throw new Error('status ' + res.status);
        return res.json();
      })
      .then(function (job) {
        if (!job) return;
        if (banner && navigator.onLine !== false) banner.hidden = true;
        update(job);
        if (!active[job.status]) { reloadOnce(); return; }
        setTimeout(poll, 2000);
      })
      .catch(function () {
        if (banner) banner.hidden = false;
        setTimeout(poll, 2000);
      });
  }
  setTimeout(poll, 2000);
})();
