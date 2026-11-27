// Parker website: theme toggle, tabs, copy buttons, latest release, hero terminal.
(function () {
  const root = document.documentElement;
  root.classList.remove('no-js');

  // ---- theme ----
  const stored = (() => { try { return localStorage.getItem('parker-theme'); } catch (e) { return null; } })();
  if (stored) root.dataset.theme = stored;
  const toggle = document.getElementById('theme');
  if (toggle) {
    toggle.addEventListener('click', () => {
      const dark = root.dataset.theme ? root.dataset.theme === 'dark'
        : matchMedia('(prefers-color-scheme: dark)').matches;
      root.dataset.theme = dark ? 'light' : 'dark';
      try { localStorage.setItem('parker-theme', root.dataset.theme); } catch (e) {}
    });
  }

  // ---- nav border on scroll ----
  const nav = document.querySelector('.nav');
  const onScroll = () => nav && nav.classList.toggle('scrolled', scrollY > 8);
  addEventListener('scroll', onScroll, { passive: true }); onScroll();

  // ---- tabs (role=tablist) ----
  document.querySelectorAll('[role=tablist]').forEach((list) => {
    const tabs = [...list.querySelectorAll('[role=tab]')];
    const select = (t) => tabs.forEach((x) => {
      const on = x === t;
      x.setAttribute('aria-selected', on);
      x.tabIndex = on ? 0 : -1;
      document.getElementById(x.getAttribute('aria-controls')).hidden = !on;
    });
    tabs.forEach((t, i) => {
      t.addEventListener('click', () => select(t));
      t.addEventListener('keydown', (e) => {
        const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
        if (d) { const n = tabs[(i + d + tabs.length) % tabs.length]; select(n); n.focus(); }
      });
    });
  });

  // ---- copy buttons ----
  document.querySelectorAll('.code').forEach((box) => {
    const pre = box.querySelector('pre');
    const btn = document.createElement('button');
    btn.className = 'copy'; btn.type = 'button'; btn.textContent = 'Copy';
    btn.addEventListener('click', async () => {
      // copy the commands only: lines starting with the prompt, without it
      const lines = pre.innerText.split('\n');
      const cmds = [];
      let cont = false; // the previous command line ended in a backslash
      for (const l of lines) {
        if (l.startsWith('$ ')) cmds.push(l.slice(2));
        else if (cont) cmds.push(l);
        else continue;
        cont = /\\$/.test(l);
      }
      const text = (cmds.length ? cmds : lines).join('\n').trim();
      try { await navigator.clipboard.writeText(text); btn.textContent = 'Copied'; }
      catch (e) { btn.textContent = 'Select & copy'; }
      setTimeout(() => (btn.textContent = 'Copy'), 1600);
    });
    box.appendChild(btn);
  });

  // ---- latest release version ----
  // The headline is the bold first line of the release notes, which the
  // release workflow takes from the annotated tag's message. (Older
  // releases carried it in the title, "v0.14.0 · headline"; still read.)
  // A release without one keeps the page's own headline only if the page
  // already names that version; otherwise it says "what's new", so an old
  // headline never sits next to a newer version.
  const verEls = document.querySelectorAll('[data-version]');
  const headEls = document.querySelectorAll('[data-release="headline"]');
  if (verEls.length || headEls.length) {
    const pageTag = (document.querySelector('[data-version="tag"]') || {}).textContent;
    fetch('https://api.github.com/repos/Ranjandas/parker/releases/latest', { headers: { Accept: 'application/vnd.github+json' } })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => {
        if (!j || !/^v\d+\.\d+\.\d+$/.test(j.tag_name)) return;
        const v = j.tag_name.slice(1);
        const fromBody = ((j.body || '').match(/^\s*\*\*(.+?)\*\*/) || [])[1];
        const fromTitle = (j.name || '').trim().replace(j.tag_name, '').replace(/^[\s·:—–-]+/, '').trim();
        const headline = (fromBody || '').trim() || fromTitle || (j.tag_name === pageTag ? null : 'what’s new');
        if (headline) headEls.forEach((el) => (el.textContent = headline));
        verEls.forEach((el) => (el.textContent = el.dataset.version === 'tag' ? 'v' + v : v));
      })
      .catch(() => {});
  }

  // ---- reveal on scroll ----
  const io = 'IntersectionObserver' in window && new IntersectionObserver((es) => {
    es.forEach((e) => { if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); } });
  }, { rootMargin: '0px 0px -8% 0px' });
  document.querySelectorAll('.reveal').forEach((el) => (io ? io.observe(el) : el.classList.add('in')));

  // ---- guide toc highlight ----
  const tocLinks = [...document.querySelectorAll('.toc a')];
  if (tocLinks.length && !document.body.classList.contains('docs') && 'IntersectionObserver' in window) {
    const map = new Map(tocLinks.map((a) => [a.getAttribute('href').slice(1), a]));
    const seen = new Set();
    const tio = new IntersectionObserver((es) => {
      es.forEach((e) => (e.isIntersecting ? seen.add(e.target.id) : seen.delete(e.target.id)));
      const first = [...map.keys()].find((id) => seen.has(id));
      if (first) tocLinks.forEach((a) => a.classList.toggle('active', a === map.get(first)));
    }, { rootMargin: '-80px 0px -55% 0px' });
    map.forEach((_, id) => { const s = document.getElementById(id); if (s) tio.observe(s); });
  }

  // ---- hero terminal: type the command, then stream the output ----
  const term = document.getElementById('hero-term');
  if (term) {
    const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
    const cmd = 'parker up minimal.yaml';
    const lines = [
      ['network: 1 network, 3 nodes', 120],
      ['  <span class="c">started</span> dc1-server-1/consul   <span class="c">pid 43324</span>', 160],
      ['  <span class="c">started</span> dc1-server-2/consul   <span class="c">pid 43331</span>', 60],
      ['  <span class="c">started</span> dc1-server-3/consul   <span class="c">pid 43338</span>', 60],
      ['  <span class="ok">ready</span>   dc1-server-2/consul   <span class="hl">4.9s</span>', 900],
      ['  <span class="ok">ready</span>   dc1-server-3/consul   <span class="hl">5.1s</span>', 200],
      ['  <span class="ok">ready</span>   dc1-server-1/consul   <span class="hl">5.5s</span>', 300],
      ['lab "minimal" is up in <span class="done-badge">5.6s</span>', 300],
      ['', 500],
      ['<span class="p">$ </span>parker exec minimal dc1/server-1 -- consul members', 0],
      ['<span class="c">Node          Address         Status  Type    Build   DC</span>', 500],
      ['dc1-server-1  10.1.0.10:8301  <span class="ok">alive</span>   server  1.22.7  dc1', 60],
      ['dc1-server-2  10.1.0.11:8301  <span class="ok">alive</span>   server  1.22.7  dc1', 60],
      ['dc1-server-3  10.1.0.12:8301  <span class="ok">alive</span>   server  1.22.7  dc1', 60],
    ];
    const out = term.querySelector('pre');
    const head = '<span class="p">$ </span>';
    const final = head + cmd + '\n' + lines.map((l) => l[0]).join('\n');
    if (reduce) { out.innerHTML = final; return; }
    let i = 0, html = head;
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    const run = async () => {
      await sleep(600);
      for (; i < cmd.length; i++) { html += cmd[i]; out.innerHTML = html + '<span class="cursor"></span>'; await sleep(45 + Math.random() * 40); }
      html += '\n';
      for (const [l, d] of lines) { await sleep(d); html += l + '\n'; out.innerHTML = html + '<span class="cursor"></span>'; }
    };
    const start = () => run().catch(() => (out.innerHTML = final));
    if ('IntersectionObserver' in window) {
      const o = new IntersectionObserver((es) => { if (es[0].isIntersecting) { o.disconnect(); start(); } });
      o.observe(term);
    } else start();
  }
})();
