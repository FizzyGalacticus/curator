'use strict';

// ── State ──────────────────────────────────────────────────────────────────
const state = {
  posts: [],
  favorites: {},    // post_id -> true
  filter: 'all',   // 'all' | 'favorites' | 'non-favorites'
  config: null,
  status: null,
  viewer: {
    active: false,
    postIndex: -1,   // index into filteredPosts()
    mediaIndex: 0,
  },
};

// ── API ───────────────────────────────────────────────────────────────────
async function apiFetch(path, opts = {}) {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...opts });
  const json = await res.json();
  if (!json.success) throw new Error(json.message || `HTTP ${res.status}`);
  return json.data;
}

const api = {
  getPosts: (filter, subreddit) => {
    const params = new URLSearchParams();
    if (filter && filter !== 'all') params.set('filter', filter);
    if (subreddit) params.set('subreddit', subreddit);
    const q = params.toString();
    return apiFetch('/api/posts' + (q ? '?' + q : ''));
  },
  toggleFavorite: (id) => apiFetch(`/api/posts/${id}/favorite`, { method: 'POST' }),
  getConfig: () => apiFetch('/api/config'),
  updateConfig: (data) => apiFetch('/api/config', { method: 'PUT', body: JSON.stringify(data) }),
  addSubreddit: (name) => apiFetch('/api/subreddits', { method: 'POST', body: JSON.stringify({ name }) }),
  removeSubreddit: (name) => apiFetch(`/api/subreddits/${name}`, { method: 'DELETE' }),
  refresh: () => apiFetch('/api/refresh', { method: 'POST' }),
  getStatus: () => apiFetch('/api/status'),
};

// ── Helpers ───────────────────────────────────────────────────────────────
function filteredPosts() {
  let posts = state.posts.slice();
  // The API already sorts (favorites first, then newest) but we re-apply here
  // so client-side favorite toggles are reflected instantly without a round-trip.
  posts.sort((a, b) => {
    const af = state.favorites[a.id] ? 1 : 0;
    const bf = state.favorites[b.id] ? 1 : 0;
    if (af !== bf) return bf - af;
    return new Date(b.created_at) - new Date(a.created_at);
  });

  if (state.filter === 'favorites') return posts.filter(p => state.favorites[p.id]);
  if (state.filter === 'non-favorites') return posts.filter(p => !state.favorites[p.id]);
  return posts;
}

function isFav(id) { return !!state.favorites[id]; }

// ── Masonry Grid ──────────────────────────────────────────────────────────
function renderGrid() {
  const grid = document.getElementById('masonry-grid');
  const empty = document.getElementById('media-empty');
  const loading = document.getElementById('media-loading');

  loading.hidden = true;
  const posts = filteredPosts();

  document.getElementById('post-count').textContent = `${posts.length} post${posts.length !== 1 ? 's' : ''}`;

  if (posts.length === 0) {
    grid.innerHTML = '';
    empty.hidden = false;
    return;
  }

  empty.hidden = true;
  grid.innerHTML = '';

  posts.forEach((post, idx) => {
    const el = buildTile(post, idx);
    grid.appendChild(el);
  });
}

function buildTile(post, postIndex) {
  const item = post.media_items[0];
  const fav = isFav(post.id);
  const isMulti = post.media_items.length > 1;

  const div = document.createElement('div');
  div.className = 'masonry-item';
  div.dataset.postId = post.id;

  // Thumb wrap
  const thumbWrap = document.createElement('div');
  thumbWrap.className = 'thumb-wrap';

  const thumbUrl = item.thumbnail || item.url;

  if (item.type === 'video') {
    const img = document.createElement('img');
    img.src = thumbUrl;
    img.alt = post.title;
    img.loading = 'lazy';
    thumbWrap.appendChild(img);

    const badge = document.createElement('div');
    badge.className = 'play-badge';
    badge.textContent = '▶';
    thumbWrap.appendChild(badge);
  } else if (item.type === 'gif') {
    const img = document.createElement('img');
    img.src = thumbUrl;
    img.alt = post.title;
    img.loading = 'lazy';
    thumbWrap.appendChild(img);

    const badge = document.createElement('div');
    badge.className = 'play-badge';
    badge.textContent = 'GIF';
    badge.style.fontSize = '11px';
    badge.style.fontWeight = '700';
    thumbWrap.appendChild(badge);
  } else {
    const img = document.createElement('img');
    img.src = thumbUrl;
    img.alt = post.title;
    img.loading = 'lazy';
    if (item.width && item.height) {
      img.style.aspectRatio = `${item.width} / ${item.height}`;
    }
    thumbWrap.appendChild(img);
  }

  // Gallery badge
  if (isMulti) {
    const badge = document.createElement('div');
    badge.className = 'gallery-badge';
    badge.textContent = `⊞ ${post.media_items.length}`;
    thumbWrap.appendChild(badge);
  }

  // Star button
  const star = document.createElement('button');
  star.className = 'tile-star' + (fav ? ' favorited' : '');
  star.title = fav ? 'Remove from favorites' : 'Add to favorites';
  star.textContent = fav ? '★' : '☆';
  star.addEventListener('click', (e) => {
    e.stopPropagation();
    handleToggleFavorite(post.id, star);
  });

  div.appendChild(thumbWrap);
  div.appendChild(star);

  // Footer
  const footer = document.createElement('div');
  footer.className = 'tile-footer';
  const sub = document.createElement('span');
  sub.className = 'tile-sub';
  sub.textContent = 'r/' + post.subreddit;
  const title = document.createElement('span');
  title.className = 'tile-title';
  title.textContent = post.title;
  footer.appendChild(sub);
  footer.appendChild(title);
  div.appendChild(footer);

  // Open viewer on click
  div.addEventListener('click', () => openViewer(postIndex));

  return div;
}

// ── Favorite toggling ─────────────────────────────────────────────────────
async function handleToggleFavorite(postId, starEl) {
  // Optimistic update
  const wasF = isFav(postId);
  state.favorites[postId] = !wasF;
  // Always re-render: favorites sort first, so any toggle shifts tile indices.
  renderGrid();
  updateStarEls(postId);

  try {
    const data = await api.toggleFavorite(postId);
    state.favorites[postId] = data.favorited;
  } catch (e) {
    // Revert on error
    state.favorites[postId] = wasF;
    console.error('Favorite toggle failed:', e);
  }
  renderGrid();
  updateStarEls(postId);
}

function updateStarEls(postId) {
  const fav = isFav(postId);
  // Grid tiles
  document.querySelectorAll(`[data-post-id="${postId}"] .tile-star`).forEach(el => {
    el.classList.toggle('favorited', fav);
    el.textContent = fav ? '★' : '☆';
  });
  // Viewer star
  if (state.viewer.active) {
    const posts = filteredPosts();
    const post = posts[state.viewer.postIndex];
    if (post && post.id === postId) {
      const btn = document.getElementById('viewer-fav-btn');
      btn.classList.toggle('favorited', fav);
      btn.textContent = fav ? '★' : '☆';
    }
  }
}

// ── Viewer ────────────────────────────────────────────────────────────────
function openViewer(postIndex, mediaIndex = 0) {
  const posts = filteredPosts();
  if (postIndex < 0 || postIndex >= posts.length) return;

  state.viewer.active = true;
  state.viewer.postIndex = postIndex;
  state.viewer.mediaIndex = mediaIndex;

  const viewer = document.getElementById('viewer');
  viewer.hidden = false;
  document.body.style.overflow = 'hidden';

  renderViewerContent();
}

function closeViewer() {
  state.viewer.active = false;
  document.getElementById('viewer').hidden = true;
  document.body.style.overflow = '';
  stopAllViewerMedia();
}

function renderViewerContent() {
  const posts = filteredPosts();
  const post = posts[state.viewer.postIndex];
  if (!post) { closeViewer(); return; }

  const mi = state.viewer.mediaIndex;
  const item = post.media_items[mi] || post.media_items[0];
  const isMulti = post.media_items.length > 1;

  // Meta
  document.getElementById('viewer-subreddit').textContent = 'r/' + post.subreddit;
  document.getElementById('viewer-title').textContent = post.title;
  document.getElementById('viewer-reddit-link').href = post.permalink;

  // Favorite state
  const favBtn = document.getElementById('viewer-fav-btn');
  const fav = isFav(post.id);
  favBtn.classList.toggle('favorited', fav);
  favBtn.textContent = fav ? '★' : '☆';

  // Post nav buttons
  const prevPost = document.getElementById('viewer-post-prev');
  const nextPost = document.getElementById('viewer-post-next');
  prevPost.disabled = state.viewer.postIndex === 0;
  nextPost.disabled = state.viewer.postIndex === posts.length - 1;

  // Gallery nav
  const galleryPrev = document.getElementById('viewer-media-prev');
  const galleryNext = document.getElementById('viewer-media-next');
  galleryPrev.hidden = !isMulti;
  galleryNext.hidden = !isMulti;
  if (isMulti) {
    galleryPrev.disabled = mi === 0;
    galleryNext.disabled = mi === post.media_items.length - 1;
  }

  // Dots
  renderDots(post, mi);

  // Media
  stopAllViewerMedia();
  const mediaDiv = document.getElementById('viewer-media');
  mediaDiv.innerHTML = '';

  if (item.type === 'video') {
    const video = document.createElement('video');
    video.src = item.url;
    video.autoplay = true;
    video.muted = true;
    video.loop = true;
    video.controls = true;
    video.playsInline = true;
    video.style.maxWidth = '100%';
    video.style.maxHeight = '100%';
    mediaDiv.appendChild(video);
    video.play().catch(() => {});
  } else if (item.type === 'gif') {
    const img = document.createElement('img');
    img.src = item.url;
    img.alt = post.title;
    mediaDiv.appendChild(img);
  } else {
    const img = document.createElement('img');
    img.src = item.url;
    img.alt = post.title;
    mediaDiv.appendChild(img);
  }
}

function stopAllViewerMedia() {
  const mediaDiv = document.getElementById('viewer-media');
  mediaDiv.querySelectorAll('video').forEach(v => { v.pause(); v.src = ''; });
}

function renderDots(post, currentIndex) {
  const dotsDiv = document.getElementById('viewer-dots');
  dotsDiv.innerHTML = '';
  if (post.media_items.length <= 1) return;

  post.media_items.forEach((_, i) => {
    const dot = document.createElement('div');
    dot.className = 'dot' + (i === currentIndex ? ' active' : '');
    dot.addEventListener('click', () => navigateMedia(i - state.viewer.mediaIndex));
    dotsDiv.appendChild(dot);
  });
}

function navigateMedia(delta) {
  const posts = filteredPosts();
  const post = posts[state.viewer.postIndex];
  if (!post) return;

  const newIndex = state.viewer.mediaIndex + delta;
  if (newIndex < 0 || newIndex >= post.media_items.length) return;
  state.viewer.mediaIndex = newIndex;
  renderViewerContent();
}

function navigatePost(delta) {
  const posts = filteredPosts();
  const newIndex = state.viewer.postIndex + delta;
  if (newIndex < 0 || newIndex >= posts.length) return;
  state.viewer.postIndex = newIndex;
  state.viewer.mediaIndex = 0;
  renderViewerContent();
}

// ── Swipe / touch handling ────────────────────────────────────────────────
let touchStart = null;

function setupSwipe(el) {
  el.addEventListener('touchstart', (e) => {
    touchStart = { x: e.touches[0].clientX, y: e.touches[0].clientY, t: Date.now() };
  }, { passive: true });

  el.addEventListener('touchend', (e) => {
    if (!touchStart) return;
    const dx = e.changedTouches[0].clientX - touchStart.x;
    const dy = e.changedTouches[0].clientY - touchStart.y;
    const dt = Date.now() - touchStart.t;
    touchStart = null;

    if (dt > 600 || (Math.abs(dx) < 30 && Math.abs(dy) < 30)) return;

    if (Math.abs(dx) > Math.abs(dy)) {
      // Horizontal swipe → gallery navigation
      if (dx < 0) navigateMedia(1);
      else navigateMedia(-1);
    } else {
      // Vertical swipe → post navigation
      if (dy < 0) navigatePost(1);  // swipe up → next post
      else navigatePost(-1);         // swipe down → prev post
    }
  }, { passive: true });
}

// ── Keyboard ─────────────────────────────────────────────────────────────
document.addEventListener('keydown', (e) => {
  if (!state.viewer.active) return;
  switch (e.key) {
    case 'Escape': closeViewer(); break;
    case 'ArrowLeft':  navigateMedia(-1); break;
    case 'ArrowRight': navigateMedia(1);  break;
    case 'ArrowUp':    navigatePost(-1);  break;
    case 'ArrowDown':  navigatePost(1);   break;
    case 'f':
    case 'F': {
      const posts = filteredPosts();
      const post = posts[state.viewer.postIndex];
      if (post) handleToggleFavorite(post.id);
      break;
    }
  }
});

// ── Config tab ────────────────────────────────────────────────────────────
function renderSubredditList() {
  const ul = document.getElementById('subreddit-list');
  const subs = state.config?.subreddits || [];

  if (subs.length === 0) {
    ul.innerHTML = '<li style="color:var(--text-muted);font-size:13px;padding:8px 0">No subreddits yet.</li>';
    return;
  }

  ul.innerHTML = '';
  subs.forEach(name => {
    const li = document.createElement('li');
    li.className = 'subreddit-item';

    const left = document.createElement('div');
    const nameEl = document.createElement('span');
    nameEl.className = 'subreddit-name';
    nameEl.textContent = 'r/' + name;

    const lastChecked = state.status?.last_checked?.[name];
    const meta = document.createElement('span');
    meta.className = 'subreddit-meta';
    meta.textContent = lastChecked && lastChecked !== 'never'
      ? 'Last checked: ' + new Date(lastChecked).toLocaleString()
      : 'Not yet checked';
    left.appendChild(nameEl);
    left.appendChild(meta);

    const actions = document.createElement('div');
    actions.className = 'subreddit-actions';

    const delBtn = document.createElement('button');
    delBtn.className = 'btn-icon-sm danger';
    delBtn.textContent = 'Remove';
    delBtn.addEventListener('click', () => handleRemoveSubreddit(name));
    actions.appendChild(delBtn);

    li.appendChild(left);
    li.appendChild(actions);
    ul.appendChild(li);
  });
}

async function handleRemoveSubreddit(name) {
  if (!confirm(`Remove r/${name}? Non-favorited posts from this subreddit will be deleted.`)) return;
  try {
    await api.removeSubreddit(name);
    await loadAll();
    renderGrid();
    renderSubredditList();
  } catch (e) {
    alert('Failed to remove subreddit: ' + e.message);
  }
}

function renderStatusPanel() {
  const panel = document.getElementById('status-panel');
  if (!state.status) { panel.innerHTML = '<span>Loading…</span>'; return; }

  const s = state.status;
  panel.innerHTML = '';

  const row = (label, value) => {
    const div = document.createElement('div');
    div.className = 'status-row';
    div.innerHTML = `<span class="status-label">${label}</span><span>${value}</span>`;
    panel.appendChild(div);
  };

  row('Posts stored', s.posts_count);
  row('Favorites', s.favorites_count);

  if (s.subreddits && s.subreddits.length > 0) {
    const hdr = document.createElement('div');
    hdr.className = 'status-label';
    hdr.style.marginTop = '8px';
    hdr.textContent = 'Last checked:';
    panel.appendChild(hdr);

    const subList = document.createElement('div');
    subList.className = 'status-sub-list';
    s.subreddits.forEach(sub => {
      const t = s.last_checked[sub];
      const row = document.createElement('div');
      row.className = 'status-sub-row';
      row.innerHTML = `<span>r/${sub}</span><span style="color:var(--text-muted)">${t === 'never' ? 'never' : new Date(t).toLocaleString()}</span>`;
      subList.appendChild(row);
    });
    panel.appendChild(subList);
  }
}

function populateSettingsForm() {
  if (!state.config) return;
  document.getElementById('cfg-interval').value = state.config.check_interval || '';
  document.getElementById('cfg-download-dir').value = state.config.download_dir || '';
  document.getElementById('cfg-max-age').value = state.config.max_post_age_days ?? '';
  document.getElementById('cfg-imgur-client-id').value = state.config.imgur_client_id || '';
}

// ── Data loading ──────────────────────────────────────────────────────────
async function loadPosts() {
  const data = await api.getPosts();
  state.posts = data || [];
  state.favorites = {};
  state.posts.forEach(p => { if (p.favorited) state.favorites[p.id] = true; });
}

async function loadAll() {
  const [cfg, status] = await Promise.all([
    api.getConfig().catch(() => null),
    api.getStatus().catch(() => null),
  ]);
  state.config = cfg;
  state.status = status;
  await loadPosts();
}

// ── Event wiring ──────────────────────────────────────────────────────────
function setupEvents() {
  // Tab switching
  document.querySelectorAll('.tab-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const tab = btn.dataset.tab;
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
      btn.classList.add('active');
      document.getElementById('tab-' + tab).classList.add('active');
      if (tab === 'config') {
        renderSubredditList();
        renderStatusPanel();
        populateSettingsForm();
      }
    });
  });

  // Filter buttons
  document.querySelectorAll('.filter-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      state.filter = btn.dataset.filter;
      document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      renderGrid();
    });
  });

  // Refresh button in toolbar
  const refreshBtn = document.getElementById('refresh-btn');
  refreshBtn.addEventListener('click', async () => {
    refreshBtn.classList.add('spinning');
    try {
      await api.refresh();
      // Give the scheduler a moment to fetch then reload.
      await new Promise(r => setTimeout(r, 3000));
      await loadAll();
      renderGrid();
    } catch (e) {
      console.error('Refresh failed:', e);
    } finally {
      refreshBtn.classList.remove('spinning');
    }
  });

  // ── Viewer events ──
  document.getElementById('viewer-backdrop').addEventListener('click', closeViewer);
  document.getElementById('viewer-close').addEventListener('click', closeViewer);

  document.getElementById('viewer-fav-btn').addEventListener('click', () => {
    const posts = filteredPosts();
    const post = posts[state.viewer.postIndex];
    if (post) handleToggleFavorite(post.id);
  });

  document.getElementById('viewer-post-prev').addEventListener('click', () => navigatePost(-1));
  document.getElementById('viewer-post-next').addEventListener('click', () => navigatePost(1));
  document.getElementById('viewer-media-prev').addEventListener('click', () => navigateMedia(-1));
  document.getElementById('viewer-media-next').addEventListener('click', () => navigateMedia(1));

  // Swipe in viewer
  setupSwipe(document.getElementById('viewer-media-wrap'));

  // ── Config events ──
  document.getElementById('add-subreddit-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const input = document.getElementById('subreddit-input');
    const name = input.value.trim().toLowerCase().replace(/^r\//, '');
    if (!name) return;
    try {
      await api.addSubreddit(name);
      input.value = '';
      await loadAll();
      renderSubredditList();
      renderStatusPanel();
    } catch (err) {
      alert('Failed to add subreddit: ' + err.message);
    }
  });

  document.getElementById('settings-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const saved = document.getElementById('settings-saved');
    try {
      await api.updateConfig({
        check_interval:    document.getElementById('cfg-interval').value.trim(),
        download_dir:      document.getElementById('cfg-download-dir').value.trim(),
        max_post_age_days: parseInt(document.getElementById('cfg-max-age').value, 10) || 30,
        imgur_client_id:   document.getElementById('cfg-imgur-client-id').value.trim(),
      });
      state.config = await api.getConfig();
      saved.hidden = false;
      setTimeout(() => { saved.hidden = true; }, 2000);
    } catch (err) {
      alert('Failed to save settings: ' + err.message);
    }
  });

  document.getElementById('refresh-now-btn').addEventListener('click', async () => {
    const btn = document.getElementById('refresh-now-btn');
    btn.textContent = 'Refreshing…';
    btn.disabled = true;
    try {
      await api.refresh();
      await new Promise(r => setTimeout(r, 4000));
      await loadAll();
      renderGrid();
      renderSubredditList();
      renderStatusPanel();
    } catch (e) {
      console.error('Refresh failed:', e);
    } finally {
      btn.textContent = 'Check subreddits now';
      btn.disabled = false;
    }
  });
}

// ── Bootstrap ─────────────────────────────────────────────────────────────
async function init() {
  const loading = document.getElementById('media-loading');
  loading.hidden = false;

  setupEvents();

  try {
    await loadAll();
  } catch (e) {
    console.error('Init failed:', e);
  }

  renderGrid();
}

init();
