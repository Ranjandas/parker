// Parker docs: search, the mobile menu and the "On this page" highlight.
(function () {
  // ---- mobile menu ----
  const menu = document.getElementById('menu');
  if (menu) {
    menu.addEventListener('click', () => {
      const open = document.body.classList.toggle('menu-open');
      menu.setAttribute('aria-expanded', open);
    });
  }

  // ---- on this page ----
  const links = [...document.querySelectorAll('.toc a')];
  if (links.length && 'IntersectionObserver' in window) {
    const byId = new Map(links.map((a) => [decodeURIComponent(a.hash.slice(1)), a]));
    const visible = new Set();
    const io = new IntersectionObserver((es) => {
      es.forEach((e) => (e.isIntersecting ? visible.add(e.target.id) : visible.delete(e.target.id)));
      const first = [...byId.keys()].find((id) => visible.has(id));
      if (first) links.forEach((a) => a.classList.toggle('active', a === byId.get(first)));
    }, { rootMargin: '-72px 0px -60% 0px' });
    byId.forEach((_, id) => { const h = document.getElementById(id); if (h) io.observe(h); });
  }

  // ---- search ----
  const input = document.getElementById('search');
  const box = document.getElementById('results');
  if (!input || !box) return;
  let index = null;
  let sel = -1;
  const load = () => index || (index = fetch('search.json').then((r) => r.json()).catch(() => []));
  const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const mark = (s, terms) => {
    let out = esc(s);
    terms.forEach((t) => { out = out.replace(new RegExp('(' + t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'ig'), '<mark>$1</mark>'); });
    return out;
  };
  const snippet = (text, terms) => {
    const lower = text.toLowerCase();
    let at = -1;
    for (const t of terms) { at = lower.indexOf(t); if (at >= 0) break; }
    const start = Math.max(0, at - 50);
    return (start > 0 ? '… ' : '') + text.slice(start, start + 150) + (text.length > start + 150 ? ' …' : '');
  };
  const render = async () => {
    const q = input.value.trim().toLowerCase();
    if (!q) { box.hidden = true; return; }
    const terms = q.split(/\s+/).filter(Boolean);
    const data = await load();
    const scored = [];
    for (const e of data) {
      const title = e.Title.toLowerCase(), head = (e.Heading || '').toLowerCase(), text = e.Text.toLowerCase();
      let score = 0, all = true;
      for (const t of terms) {
        let s = 0;
        if (title.includes(t)) s += 8;
        if (head.includes(t)) s += 6;
        if (text.includes(t)) s += 1 + Math.min(3, text.split(t).length - 2);
        if (!s) { all = false; break; }
        score += s;
      }
      if (all) scored.push([score + (e.URL.includes('#') ? 0 : 2), e]);
    }
    scored.sort((a, b) => b[0] - a[0]);
    const top = scored.slice(0, 10);
    sel = -1;
    box.innerHTML = top.length
      ? top.map(([, e]) => `<a href="${e.URL}" role="option"><b>${mark(e.Heading && e.Heading !== e.Title ? e.Heading : e.Title, terms)}</b><small>${esc(e.Section)} › ${esc(e.Title)}</small><span>${mark(snippet(e.Text, terms), terms)}</span></a>`).join('')
      : '<div class="none">No matches. Try a command or a key, like <code>upstreams</code>.</div>';
    box.hidden = false;
  };
  input.addEventListener('focus', load);
  input.addEventListener('input', render);
  input.addEventListener('keydown', (e) => {
    const items = [...box.querySelectorAll('a')];
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!items.length) return;
      sel = (sel + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      items.forEach((a, i) => a.setAttribute('aria-selected', i === sel));
      items[sel].scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'Enter') {
      const a = items[sel >= 0 ? sel : 0];
      if (a) location.href = a.href;
    } else if (e.key === 'Escape') {
      box.hidden = true; input.blur();
    }
  });
  document.addEventListener('click', (e) => { if (!e.target.closest('.search')) box.hidden = true; });
  document.addEventListener('keydown', (e) => {
    if (e.key === '/' && document.activeElement !== input && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) {
      e.preventDefault(); input.focus();
    }
  });
})();
