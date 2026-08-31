// app.js — small, progressive-enhancement-only scripts.
// No framework, no build step. Plain DOM.
//
// Everything here degrades gracefully: with JavaScript disabled the app
// still works (search submits via its button, and saved UI preferences
// still apply because the server renders them onto the page — see the
// data-* attributes on <html>). JS only adds instant switching and
// writes preference changes back to the server.

(function () {
  // ── Filter form ─────────────────────────────────────────────────
  // Search is button-driven: typing does NOT fire requests. Large
  // libraries made the old per-keystroke auto-submit (350 ms debounce)
  // janky, so now a request happens only when the user presses Enter,
  // clicks Search, or clicks the × clear button. The filter <select>s
  // still auto-submit on change — that's one deliberate action, not a
  // stream of keystrokes.
  var form = document.getElementById('filter-form');
  if (form) {
    var selects = form.querySelectorAll('select');
    selects.forEach(function (s) {
      s.addEventListener('change', function () { form.submit(); });
    });
    var search = document.getElementById('search');
    var clear = document.getElementById('search-clear');
    if (search && clear) {
      clear.addEventListener('click', function () {
        search.value = '';
        form.submit();
      });
    }
  }

  // ── Server-side UI preferences ──────────────────────────────
  // Layout, ribbon, emblem and theme settings live in the SERVER's
  // SQLite settings table (POST /api/settings), so they follow the
  // database instead of the browser: every browser or device opening
  // the app renders the same look. The current values arrive
  // server-rendered — data-* attributes on <html> plus the #ft-settings
  // JSON blob — and changes made below are applied immediately and
  // persisted with a short debounce, so a slider drag fires one request
  // per gesture rather than one per pixel.
  var FT = {};
  try {
    FT = JSON.parse(document.getElementById('ft-settings').textContent) || {};
  } catch (e) { FT = {}; }

  // One-time upgrade path: for preference groups the server doesn't have
  // yet, adopt the per-browser localStorage prefs saved by
  // pre-server-settings builds, queue them for the server, and delete
  // the keys once stored. First browser to load wins; afterwards the
  // server is the single source of truth. Queued through the same
  // debounce as user changes, so a click landing milliseconds later
  // simply overwrites the queued legacy value (no write race).
  //
  // NOTE: pendingPatch / flushTimer MUST be declared (and initialized)
  // before this IIFE runs — it calls persistPrefs immediately, and a
  // hoisted-but-uninitialized `var` would throw "Cannot set properties
  // of undefined", aborting the entire script for upgrading users.
  var legacyCleanup = null;
  var pendingPatch = {};
  var flushTimer = null;
  (function mergeLegacyPrefs() {
    var legacyGroups = {};
    try {
      if (!FT.layout) {
        var l = localStorage.getItem('fic-tally:layout');
        if (l === 'compact' || l === 'details') { FT.layout = l; legacyGroups.layout = l; }
      }
      ['ribbon', 'emblem'].forEach(function (g) {
        if (FT[g]) return;
        var raw = localStorage.getItem('fic-tally:' + g);
        if (!raw) return;
        var c = JSON.parse(raw);
        if (c && typeof c === 'object') { FT[g] = c; legacyGroups[g] = c; }
      });
    } catch (e) {}
    if (!Object.keys(legacyGroups).length) return;
    legacyCleanup = ['fic-tally:layout', 'fic-tally:ribbon', 'fic-tally:emblem'];
    persistPrefs(legacyGroups);
  })();

  function flushPrefs(useBeacon) {
    var keys = Object.keys(pendingPatch);
    if (!keys.length) return;
    var body = JSON.stringify(pendingPatch);
    var cleanup = legacyCleanup;
    pendingPatch = {};
    legacyCleanup = null;
    flushTimer = null;
    function removeLegacyKeys() {
      (cleanup || []).forEach(function (k) {
        try { localStorage.removeItem(k); } catch (e) {}
      });
    }
    if (useBeacon && navigator.sendBeacon) {
      // Beacons cannot send application/json; the server parses the body
      // as JSON regardless of Content-Type.
      navigator.sendBeacon('/api/settings',
        new Blob([body], { type: 'text/plain;charset=utf-8' }));
      removeLegacyKeys();
    } else {
      fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: body
      }).then(function () {
        removeLegacyKeys();
      }).catch(function () {
        // Server unreachable: keep the applied look, and keep the legacy
        // keys so the migration is retried on the next load.
        legacyCleanup = cleanup;
      });
    }
  }

  // persistPrefs queues a settings patch and debounces the POST. Both
  // clicks and slider drags funnel through here; a 250 ms idle window
  // coalesces a drag gesture into a single request.
  function persistPrefs(patch) {
    Object.keys(patch).forEach(function (k) { pendingPatch[k] = patch[k]; });
    if (flushTimer) clearTimeout(flushTimer);
    flushTimer = setTimeout(flushPrefs, 250);
  }

  // If the tab closes mid-debounce, flush what's pending (as a beacon)
  // so the very last change isn't lost.
  window.addEventListener('pagehide', function () {
    if (flushTimer) { clearTimeout(flushTimer); flushPrefs(true); }
  });

  // ── Saved default sort ─────────────────────────────────────────
  // The star next to the Sort select saves the current sorting as the
  // server-side default (library settings group). The server applies the
  // saved sort whenever the library loads without an explicit ?sort= —
  // in every browser, since the setting follows the database. Active
  // (gold) star = the current sorting IS the default.
  var sortSel = document.getElementById('sort-by');
  var sortStar = document.getElementById('sort-default');
  if (sortSel && sortStar) {
    var SORT_LABELS = { last_read: 'Recently active', title: 'Title', rating: 'Rating', updated: 'Last updated' };
    var savedSort = (FT.library && typeof FT.library.sort === 'string' &&
      ['last_read', 'title', 'rating', 'updated'].indexOf(FT.library.sort) >= 0)
      ? FT.library.sort : 'updated';
    function syncSortStar() {
      var active = sortSel.value === savedSort;
      sortStar.classList.toggle('active', active);
      sortStar.setAttribute('aria-pressed', active ? 'true' : 'false');
      sortStar.title = active
        ? '"' + (SORT_LABELS[savedSort] || savedSort) + '" is your default sorting'
        : 'Save "' + (SORT_LABELS[sortSel.value] || sortSel.value) + '" as your default sorting';
    }
    syncSortStar();
    sortStar.addEventListener('click', function () {
      savedSort = sortSel.value;
      FT.library = { sort: savedSort };               // keep runtime state in sync
      persistPrefs({ library: { sort: savedSort } }); // ...and store it server-side
      syncSortStar();
    });
  }

  // ── Library layout switch (default / compact / details) ──────────
  // The extra fields for the details layout are rendered in every card
  // and shown/hidden by CSS on <html data-layout="…">, so switching is
  // instant. The preference is stored server-side and also rendered onto
  // <html> by the server — it applies in every browser and even without
  // JavaScript.
  var layoutBtns = document.querySelectorAll('.layout-btn');
  function applyLayout(l, save) {
    if (l !== 'compact' && l !== 'details') l = 'default';
    document.documentElement.setAttribute('data-layout', l);
    layoutBtns.forEach(function (b) {
      b.classList.toggle('active', b.getAttribute('data-layout') === l);
    });
    if (save !== false) persistPrefs({ layout: l });
  }
  if (layoutBtns.length) {
    layoutBtns.forEach(function (b) {
      b.addEventListener('click', function () {
        applyLayout(b.getAttribute('data-layout'));
      });
    });
    // FT.layout (server value, or the legacy pref merged above) matches
    // the attribute the server already rendered — this just syncs the
    // active button.
    applyLayout(FT.layout, false);
  }

  // ── Progress-ribbon customization ────────────────────────────────
  // The "Bookmark style" popover (library page) configures the progress
  // ribbon: color, transparency, width, shape (tag / line / triangle /
  // round) and side (left / right). Settings are stored server-side
  // (FT.ribbon ← POST /api/settings) and applied as CSS custom properties
  // + data attributes on <html>; layout.html re-applies them pre-paint.
  // The panel is present only on the library page, but the ribbon it
  // configures renders on the detail page too.
  var bmPanel = document.getElementById('bm-panel');
  var bmToggle = document.getElementById('bm-toggle');
  var BM_DEFAULTS = { color: '', opacity: 1, width: 11, shape: 'tag', side: 'left' };

  function bmConfig() {
    var c = (FT.ribbon && typeof FT.ribbon === 'object') ? FT.ribbon : {};
    return {
      color: typeof c.color === 'string' ? c.color : BM_DEFAULTS.color,
      opacity: (typeof c.opacity === 'number' && c.opacity >= 0.15 && c.opacity <= 1)
        ? c.opacity : BM_DEFAULTS.opacity,
      width: (typeof c.width === 'number' && c.width >= 3 && c.width <= 20)
        ? c.width : BM_DEFAULTS.width,
      shape: ['tag', 'line', 'triangle', 'round'].indexOf(c.shape) >= 0
        ? c.shape : BM_DEFAULTS.shape,
      side: c.side === 'right' ? 'right' : BM_DEFAULTS.side
    };
  }

  function bmApply(cfg) {
    var de = document.documentElement;
    if (cfg.color) de.style.setProperty('--bm-color', cfg.color);
    else de.style.removeProperty('--bm-color'); // fall back to theme red
    de.style.setProperty('--bm-opacity', String(cfg.opacity));
    de.style.setProperty('--bm-width', cfg.width + 'px');
    de.setAttribute('data-ribbon-shape', cfg.shape);
    de.setAttribute('data-ribbon-side', cfg.side);
  }

  if (bmPanel && bmToggle) {
    var q = function (sel) { return bmPanel.querySelector(sel); };
    var qa = function (sel) { return bmPanel.querySelectorAll(sel); };
    var colorInput = q('#bm-color');
    var opacityInput = q('#bm-opacity');
    var opacityVal = q('#bm-opacity-val');
    var widthInput = q('#bm-width');
    var widthVal = q('#bm-width-val');

    function syncUI(cfg) {
      qa('.bm-swatch').forEach(function (b) {
        var c = b.getAttribute('data-color');
        // No saved color = theme crimson, which is the first swatch.
        b.classList.toggle('active', c === cfg.color ||
          (cfg.color === '' && c === '#a8342a'));
      });
      colorInput.value = cfg.color || '#a8342a';
      opacityInput.value = Math.round(cfg.opacity * 100);
      opacityVal.textContent = Math.round(cfg.opacity * 100) + '%';
      widthInput.value = cfg.width;
      widthVal.textContent = cfg.width + 'px';
      qa('.bm-shape').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-shape') === cfg.shape);
      });
      qa('.bm-side').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-side') === cfg.side);
      });
    }

    function update(patch) {
      var cfg = bmConfig();
      Object.keys(patch).forEach(function (k) { cfg[k] = patch[k]; });
      FT.ribbon = cfg;               // keep runtime state in sync
      bmApply(cfg);
      persistPrefs({ ribbon: cfg }); // ...and store it server-side
      syncUI(cfg);
    }

    qa('.bm-swatch').forEach(function (b) {
      b.addEventListener('click', function () {
        update({ color: b.getAttribute('data-color') });
      });
    });
    colorInput.addEventListener('input', function () {
      update({ color: colorInput.value });
    });
    opacityInput.addEventListener('input', function () {
      update({ opacity: parseInt(opacityInput.value, 10) / 100 });
    });
    widthInput.addEventListener('input', function () {
      update({ width: parseInt(widthInput.value, 10) });
    });
    qa('.bm-shape').forEach(function (b) {
      b.addEventListener('click', function () {
        update({ shape: b.getAttribute('data-shape') });
      });
    });
    qa('.bm-side').forEach(function (b) {
      b.addEventListener('click', function () {
        update({ side: b.getAttribute('data-side') });
      });
    });
    q('#bm-reset').addEventListener('click', function () {
      var cfg = { color: BM_DEFAULTS.color, opacity: BM_DEFAULTS.opacity,
                  width: BM_DEFAULTS.width, shape: BM_DEFAULTS.shape,
                  side: BM_DEFAULTS.side };
      FT.ribbon = cfg;               // keep runtime state in sync
      bmApply(cfg);
      persistPrefs({ ribbon: cfg }); // ...and store it server-side
      syncUI(cfg);
    });

    // Reflect whatever the pre-paint script already applied.
    var initial = bmConfig();
    bmApply(initial);
    syncUI(initial);

    // Open/close the popover: toggle button, Esc, click outside. Opening
    // one settings popover closes the other so they never overlap.
    function setPanel(open) {
      bmPanel.hidden = !open;
      bmToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open && emPanel && !emPanel.hidden) {
        emPanel.hidden = true;
        if (emToggle) emToggle.setAttribute('aria-expanded', 'false');
      }
    }
    bmToggle.addEventListener('click', function () { setPanel(bmPanel.hidden); });
    document.addEventListener('click', function (ev) {
      if (!bmPanel.hidden && !bmPanel.contains(ev.target) &&
          !bmToggle.contains(ev.target)) {
        setPanel(false);
      }
    });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' && !bmPanel.hidden) setPanel(false);
    });
  }

  // ── Completed-emblem configuration ──────────────────────────────
  // The "Emblem style" popover (library page) configures the completion
  // seal: show/hide, style (seal / check / star), color, size,
  // transparency, and corner. Same server-side pattern as the ribbon
  // settings (FT.emblem ← POST /api/settings), applied as CSS custom
  // properties + data attributes on <html> and re-applied pre-paint by
  // layout.html. The emblem itself is server-rendered whenever reading
  // and publication are both "completed"; these settings only change
  // how it looks.
  var emPanel = document.getElementById('em-panel');
  var emToggle = document.getElementById('em-toggle');
  var EM_DEFAULTS = { show: 'on', style: 'seal', color: '', size: 26, opacity: 1, pos: 'br' };

  function emConfig() {
    var c = (FT.emblem && typeof FT.emblem === 'object') ? FT.emblem : {};
    return {
      show: c.show === 'off' ? 'off' : 'on',
      style: ['seal', 'check', 'star'].indexOf(c.style) >= 0 ? c.style : EM_DEFAULTS.style,
      color: typeof c.color === 'string' ? c.color : EM_DEFAULTS.color,
      size: (typeof c.size === 'number' && c.size >= 16 && c.size <= 40)
        ? c.size : EM_DEFAULTS.size,
      opacity: (typeof c.opacity === 'number' && c.opacity >= 0.15 && c.opacity <= 1)
        ? c.opacity : EM_DEFAULTS.opacity,
      pos: ['tl', 'tr', 'bl', 'br'].indexOf(c.pos) >= 0 ? c.pos : EM_DEFAULTS.pos
    };
  }

  function emApply(cfg) {
    var de = document.documentElement;
    if (cfg.show === 'off') de.setAttribute('data-emblem-hidden', '1');
    else de.removeAttribute('data-emblem-hidden');
    de.setAttribute('data-emblem-style', cfg.style);
    de.setAttribute('data-emblem-pos', cfg.pos);
    if (cfg.color) de.style.setProperty('--em-color', cfg.color);
    else de.style.removeProperty('--em-color'); // fall back to default gold
    // Only pin size/opacity when the user actually customized them: an
    // inline property would otherwise out-rank the stylesheet rule that
    // shrinks the emblem on compact cards (html[data-layout="compact"]).
    if (cfg.size !== EM_DEFAULTS.size) de.style.setProperty('--em-size', cfg.size + 'px');
    else de.style.removeProperty('--em-size');
    if (cfg.opacity !== EM_DEFAULTS.opacity) de.style.setProperty('--em-opacity', String(cfg.opacity));
    else de.style.removeProperty('--em-opacity');
  }

  if (emPanel && emToggle) {
    var eq = function (sel) { return emPanel.querySelector(sel); };
    var eqa = function (sel) { return emPanel.querySelectorAll(sel); };
    var emColorInput = eq('#em-color');
    var emSizeInput = eq('#em-size');
    var emSizeVal = eq('#em-size-val');
    var emOpacityInput = eq('#em-opacity');
    var emOpacityVal = eq('#em-opacity-val');

    function emSyncUI(cfg) {
      eqa('.em-show').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-show') === cfg.show);
      });
      eqa('.em-style').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-style') === cfg.style);
      });
      eqa('.em-swatch').forEach(function (b) {
        var c = b.getAttribute('data-color');
        // No saved color = default gold, which is the first swatch.
        b.classList.toggle('active', c === cfg.color ||
          (cfg.color === '' && c === '#d9a521'));
      });
      emColorInput.value = cfg.color || '#d9a521';
      emSizeInput.value = cfg.size;
      emSizeVal.textContent = cfg.size + 'px';
      emOpacityInput.value = Math.round(cfg.opacity * 100);
      emOpacityVal.textContent = Math.round(cfg.opacity * 100) + '%';
      eqa('.em-pos').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-pos') === cfg.pos);
      });
    }

    function emUpdate(patch) {
      var cfg = emConfig();
      Object.keys(patch).forEach(function (k) { cfg[k] = patch[k]; });
      FT.emblem = cfg;               // keep runtime state in sync
      emApply(cfg);
      persistPrefs({ emblem: cfg }); // ...and store it server-side
      emSyncUI(cfg);
    }

    eqa('.em-show').forEach(function (b) {
      b.addEventListener('click', function () {
        emUpdate({ show: b.getAttribute('data-show') });
      });
    });
    eqa('.em-style').forEach(function (b) {
      b.addEventListener('click', function () {
        emUpdate({ style: b.getAttribute('data-style') });
      });
    });
    eqa('.em-swatch').forEach(function (b) {
      b.addEventListener('click', function () {
        emUpdate({ color: b.getAttribute('data-color') });
      });
    });
    emColorInput.addEventListener('input', function () {
      emUpdate({ color: emColorInput.value });
    });
    emSizeInput.addEventListener('input', function () {
      emUpdate({ size: parseInt(emSizeInput.value, 10) });
    });
    emOpacityInput.addEventListener('input', function () {
      emUpdate({ opacity: parseInt(emOpacityInput.value, 10) / 100 });
    });
    eqa('.em-pos').forEach(function (b) {
      b.addEventListener('click', function () {
        emUpdate({ pos: b.getAttribute('data-pos') });
      });
    });
    eq('#em-reset').addEventListener('click', function () {
      var cfg = { show: EM_DEFAULTS.show, style: EM_DEFAULTS.style,
                  color: EM_DEFAULTS.color, size: EM_DEFAULTS.size,
                  opacity: EM_DEFAULTS.opacity, pos: EM_DEFAULTS.pos };
      FT.emblem = cfg;               // keep runtime state in sync
      emApply(cfg);
      persistPrefs({ emblem: cfg }); // ...and store it server-side
      emSyncUI(cfg);
    });

    // Reflect whatever the pre-paint script already applied.
    var emInitial = emConfig();
    emApply(emInitial);
    emSyncUI(emInitial);

    // Open/close the popover: toggle button, Esc, click outside. Opening
    // one settings popover closes the other so they never overlap.
    function setEmPanel(open) {
      emPanel.hidden = !open;
      emToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open && bmPanel && !bmPanel.hidden) {
        bmPanel.hidden = true;
        if (bmToggle) bmToggle.setAttribute('aria-expanded', 'false');
      }
    }
    emToggle.addEventListener('click', function () { setEmPanel(emPanel.hidden); });
    document.addEventListener('click', function (ev) {
      if (!emPanel.hidden && !emPanel.contains(ev.target) &&
          !emToggle.contains(ev.target)) {
        setEmPanel(false);
      }
    });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' && !emPanel.hidden) setEmPanel(false);
    });
  }

  // ── Card tag toggling (multi-tag filter) ───────────────────
  // Mini-tags on library cards (visible in the details layout) toggle tag
  // filters: click adds the tag to the ?tag= list (comma-separated, AND
  // semantics); clicking an active (highlighted) tag removes it again.
  // The chips under the toolbar offer the same removal without JS — this
  // adds the no-keyboard-shortcuts-needed add path. A click must not open
  // the series the card links to, hence stopPropagation.
  var tagToggles = document.querySelectorAll('.card-tags .mini-tag');
  if (tagToggles.length) {
    Array.prototype.forEach.call(tagToggles, function (el) {
      var tag = (el.textContent || '').trim();
      if (!tag) return;
      el.classList.add('tag-toggle');
      el.setAttribute('role', 'button');
      el.setAttribute('tabindex', '0');
      el.title = el.classList.contains('tag-active')
        ? 'Remove tag filter ' + tag : 'Filter by tag ' + tag;
      function toggleTag(ev) {
        ev.preventDefault();
        ev.stopPropagation();
        var params = new URLSearchParams(window.location.search);
        var list = (params.get('tag') || '').split(',').filter(Boolean);
        var idx = -1;
        for (var i = 0; i < list.length; i++) {
          if (list[i].trim().toLowerCase() === tag.toLowerCase()) { idx = i; break; }
        }
        if (idx >= 0) list.splice(idx, 1); else list.push(tag);
        if (list.length) params.set('tag', list.join(',')); else params.delete('tag');
        var qs = params.toString();
        window.location.href = '/' + (qs ? '?' + qs : '');
      }
      el.addEventListener('click', toggleTag);
      el.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') toggleTag(ev);
      });
    });
  }

  // ── Theme toggle back-redirect ──────────────────────────────────
  // The theme form has a hidden "back" field; populate it with the current
  // path so the POST /theme handler can redirect back to where the user
  // was, not to "/" every time.
  var themeForm = document.querySelector('.theme-form');
  if (themeForm) {
    var back = themeForm.querySelector('input[name=back]');
    if (back) back.value = window.location.pathname + window.location.search;
  }

  // ── Tag autocomplete (edit form) ────────────────────────────────
  // The Tags input carries the library's existing tags as a JSON array in
  // data-tags (rendered by the server). While the user types the tag being
  // composed (the text after the last comma), matching tags — case-
  // insensitive prefix — show as suggestions. Arrow keys move, Enter/Tab
  // or click completes the token in place. Esc closes. Everything is a
  // progressive enhancement: plain typing works without any of this.
  var tagsInput = document.getElementById('tags');
  var tagAc = document.getElementById('tag-ac');
  if (tagsInput && tagAc) {
    var existing = [];
    try {
      var parsed = JSON.parse(tagsInput.getAttribute('data-tags') || '[]');
      if (Object.prototype.toString.call(parsed) === '[object Array]') {
        existing = parsed.filter(function (t) { return typeof t === 'string' && t; });
      }
    } catch (e) { existing = []; }
    var acItems = [];      // suggestion strings, in display order
    var acIndex = -1;      // keyboard-selected item

    function currentToken() {
      var v = tagsInput.value;
      var comma = v.lastIndexOf(',');
      return v.slice(comma + 1);
    }
    function tokenStart() {
      var v = tagsInput.value;
      return v.lastIndexOf(',') + 1;
    }
    function renderAc(list, token) {
      acItems = list;
      acIndex = list.length ? 0 : -1;
      tagAc.innerHTML = '';
      if (!list.length) {
        if (token.trim() === '') { tagAc.hidden = true; return; }
        var empty = document.createElement('div');
        empty.className = 'tag-ac-empty';
        empty.textContent = 'No matching tag';
        tagAc.appendChild(empty);
      } else {
        list.forEach(function (t, i) {
          var b = document.createElement('button');
          b.type = 'button';
          b.className = 'tag-ac-item' + (i === acIndex ? ' active' : '');
          b.textContent = t;
          b.addEventListener('mousedown', function (ev) {
            // mousedown beats blur so the click registers before the
            // popover hides itself.
            ev.preventDefault();
            accept(t);
          });
          tagAc.appendChild(b);
        });
      }
      tagAc.hidden = false;
    }
    function accept(tag) {
      var v = tagsInput.value;
      var start = tokenStart();
      // Preserve any leading whitespace before the token, join with ", ".
      var prefix = v.slice(0, start);
      tagsInput.value = prefix.replace(/,\s*$/, '') + (prefix.trim() === '' ? '' : ', ') + tag + ', ';
      tagsInput.focus();
      tagAc.hidden = true;
      acItems = [];
    }
    function refreshAc() {
      var token = currentToken();
      if (token.trim() === '') { tagAc.hidden = true; acItems = []; return; }
      var lower = token.trim().toLowerCase();
      var seen = {};
      var cur = tagsInput.value.split(',').map(function (s) { return s.trim().toLowerCase(); });
      var matches = existing.filter(function (t) {
        var tl = t.toLowerCase();
        if (!tl || seen[tl] || cur.indexOf(tl) >= 0) return false; // typed already
        if (tl === lower) return false; // exact → nothing to complete
        seen[tl] = true;
        return tl.indexOf(lower) === 0;
      });
      renderAc(matches.slice(0, 6), token);
    }
    tagsInput.addEventListener('input', refreshAc);
    tagsInput.addEventListener('focus', refreshAc);
    tagsInput.addEventListener('blur', function () {
      // Slight delay so a mousedown on a suggestion still lands.
      setTimeout(function () { tagAc.hidden = true; }, 120);
    });
    tagsInput.addEventListener('keydown', function (ev) {
      if (tagAc.hidden || !acItems.length) {
        if (ev.key === 'Escape') tagAc.hidden = true;
        return;
      }
      if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
        ev.preventDefault();
        acIndex = ev.key === 'ArrowDown'
          ? (acIndex + 1) % acItems.length
          : (acIndex - 1 + acItems.length) % acItems.length;
        var btns = tagAc.querySelectorAll('.tag-ac-item');
        btns.forEach(function (b, i) { b.classList.toggle('active', i === acIndex); });
      } else if (ev.key === 'Enter' || ev.key === 'Tab') {
        if (acIndex >= 0 && acIndex < acItems.length) {
          ev.preventDefault();
          accept(acItems[acIndex]);
        }
      } else if (ev.key === 'Escape') {
        tagAc.hidden = true;
      }
    });
  }

  // ── Cover drop zone + instant preview (edit form) ───────────────
  // The preview box is a <label for=cover>, so clicking it opens the file
  // picker with no JS. This adds: drag-an-image-onto-the-box (fills the
  // real file input from the drop), and an instant local preview via a
  // blob URL so the user sees the cover they're about to upload — the
  // actual save is still the Upload button (plain form POST).
  var dropZone = document.getElementById('cover-drop');
  var coverInput = document.getElementById('cover');
  if (dropZone && coverInput) {
    var previewImg = document.getElementById('cover-preview-img');
    var previewMono = document.getElementById('cover-preview-mono');
    var previewLabel = dropZone.querySelector('.cover-preview');

    function showPreview(file) {
      if (!file || file.type.indexOf('image/') !== 0) return;
      var url = URL.createObjectURL(file);
      if (previewImg) {
        previewImg.src = url;
      } else if (previewMono) {
        // No cover yet: replace the initial-letter placeholder with the
        // picked image, keeping the mono span around for a future "remove".
        var img = document.createElement('img');
        img.className = 'cover-img';
        img.id = 'cover-preview-img';
        img.src = url;
        img.alt = '';
        previewMono.style.display = 'none';
        previewMono.parentNode.insertBefore(img, previewMono);
        previewImg = img;
      }
    }
    function isImage(dt) {
      return dt && dt.files && dt.files.length && dt.files[0].type.indexOf('image/') === 0;
    }
    ['dragenter', 'dragover'].forEach(function (n) {
      dropZone.addEventListener(n, function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        dropZone.classList.add('dragover');
      });
    });
    ['dragleave', 'dragend'].forEach(function (n) {
      dropZone.addEventListener(n, function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        dropZone.classList.remove('dragover');
      });
    });
    dropZone.addEventListener('drop', function (ev) {
      ev.preventDefault();
      ev.stopPropagation();
      dropZone.classList.remove('dragover');
      if (!isImage(ev.dataTransfer)) return;
      try {
        // Fill the real input so the Upload button commits exactly what
        // was dropped (DataTransfer file lists are constructible in all
        // evergreen browsers).
        coverInput.files = ev.dataTransfer.files;
      } catch (e) { /* older engines: the user can still pick via click */ }
      showPreview(ev.dataTransfer.files[0]);
    });
    coverInput.addEventListener('change', function () {
      if (coverInput.files && coverInput.files[0]) showPreview(coverInput.files[0]);
    });
    if (previewLabel && coverInput.required) {
      // The input is visually the no-JS fallback path; once the drop zone
      // is interactive, picking via click/drop sets it and the badge shows
      // the state. No behavior change needed — just keep the label live.
      previewLabel.setAttribute('data-drop-ready', '1');
    }
  }

  // ── Bulk-selection counter (library) ────────────────────────────
  // The bulk bar is fully functional without JS (checkboxes + select +
  // submit). This only live-updates the "N selected" hint as boxes are
  // ticked, so the user knows what the Apply button will touch.
  var bulkForm = document.getElementById('bulk-form');
  if (bulkForm) {
    var bulkCount = document.getElementById('bulk-count');
    var boxes = bulkForm.querySelectorAll('input[name="series_ids"]');
    function syncBulkCount() {
      if (!bulkCount) return;
      var n = bulkForm.querySelectorAll('input[name="series_ids"]:checked').length;
      bulkCount.textContent = n === 1 ? '1 series selected' : n + ' series selected';
      bulkCount.hidden = n === 0;
    }
    boxes.forEach(function (b) { b.addEventListener('change', syncBulkCount); });
    syncBulkCount();
  }

  // ── Chapter input: +1 advances by 1 then submits ───────────────────
  // Already handled by the form's submit buttons in markup; nothing to do
  // here. Kept as a hook in case we want optimistic UX later.
})();
