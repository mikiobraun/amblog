// Reactions and readers on ampress post pages (see reactions.go).
//
// The page renders the counts; this script makes the buttons work and counts
// a read once half the page has been seen for 15 seconds. The reader is
// anonymous: a random token kept in localStorage.
(function () {
  'use strict';

  const box = document.querySelector('.reactions[data-api]');
  if (!box) return;
  const api = box.dataset.api;

  function readerToken() {
    try {
      let token = localStorage.getItem('ampress-reader');
      if (!token) {
        const bytes = crypto.getRandomValues(new Uint8Array(16));
        token = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
        localStorage.setItem('ampress-reader', token);
      }
      return token;
    } catch (e) {
      return null; // storage blocked: the counts stay visible, nothing else
    }
  }
  const reader = readerToken();
  if (!reader) return;

  function render(state) {
    for (const r of state.reactions) {
      const button = box.querySelector(`button[data-emoji="${CSS.escape(r.emoji)}"]`);
      if (!button) continue;
      const mine = state.mine.includes(r.emoji);
      button.querySelector('.count').textContent = r.count;
      button.classList.toggle('mine', mine);
      button.setAttribute('aria-pressed', mine);
      button.disabled = false;
    }
    const reads = box.querySelector('.reads');
    if (reads) reads.textContent = state.reads === 1 ? '1 reader' : `${state.reads} readers`;
  }

  async function call(path, body) {
    const init = body === undefined ? {} : {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    };
    const res = await fetch(api + path, init);
    if (!res.ok) throw new Error(`${path}: ${res.status}`);
    render(await res.json());
  }

  box.addEventListener('click', e => {
    const button = e.target.closest('button[data-emoji]');
    if (!button) return;
    button.disabled = true;
    call('/reactions', { reader, emoji: button.dataset.emoji })
      .catch(() => { button.disabled = false; });
  });
  call('?reader=' + encodeURIComponent(reader)).catch(() => {});

  // A read: half the page seen, and 15 seconds on it.
  const start = Date.now();
  let counted = false;
  function check() {
    if (counted) return;
    const scrollable = document.documentElement.scrollHeight - window.innerHeight;
    const depth = scrollable <= 0 ? 1 : window.scrollY / scrollable;
    if (depth >= 0.5 && Date.now() - start >= 15000) {
      counted = true;
      window.removeEventListener('scroll', check);
      clearInterval(timer);
      call('/reads', { reader }).catch(() => {});
    }
  }
  window.addEventListener('scroll', check, { passive: true });
  const timer = setInterval(check, 1000);
})();
