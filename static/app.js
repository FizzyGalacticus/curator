'use strict';

// ── State ──────────────────────────────────────────────────────────────────
const state = {
  view: 'home',            // 'home' | 'list' | 'settings'
  currentListId: null,
  currentList: null,        // {id, name, subreddits}
  lists: [],                // homepage summaries
  posts: [],
  favorites: {},             // post_id -> true
  filter: 'all',            // 'all' | 'favorites' | 'non-favorites'
  status: null,
  settings: null,            // global settings (check_interval, download_dir, ...)
  videoUnmuted: false,       // in-memory only: resets to false on page reload/refresh
  viewer: {
    active: false,
    postIndex: -1,           // index into filteredPosts()
    mediaIndex: 0,
  },
};

// ── API ───────────────────────────────────────────────────────────────────
const apiFetch = async (path, opts = {}) => {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...opts });
  const json = await res.json();
  if (!json.success) throw new Error(json.message || `HTTP ${res.status}`);
  return json.data;
};

const api = {
  getLists: () => apiFetch('/api/lists'),
  createList: (name, subreddits) => apiFetch('/api/lists', { method: 'POST', body: JSON.stringify({ name, subreddits }) }),
  getList: (listId) => apiFetch(`/api/lists/${listId}`),
  renameList: (listId, name) => apiFetch(`/api/lists/${listId}`, { method: 'PUT', body: JSON.stringify({ name }) }),
  deleteList: (listId) => apiFetch(`/api/lists/${listId}`, { method: 'DELETE' }),
  getPosts: (listId, filter, subreddit) => {
    const params = new URLSearchParams();
    if (filter && filter !== 'all') params.set('filter', filter);
    if (subreddit) params.set('subreddit', subreddit);
    const q = params.toString();
    return apiFetch(`/api/lists/${listId}/posts` + (q ? '?' + q : ''));
  },
  toggleFavorite: (listId, postId) => apiFetch(`/api/lists/${listId}/posts/${postId}/favorite`, { method: 'POST' }),
  addSubredditToList: (listId, name) => apiFetch(`/api/lists/${listId}/subreddits`, { method: 'POST', body: JSON.stringify({ name }) }),
  removeSubredditFromList: (listId, name) => apiFetch(`/api/lists/${listId}/subreddits/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  addFlickrGroupToList: (listId, name) => apiFetch(`/api/lists/${listId}/flickr-groups`, { method: 'POST', body: JSON.stringify({ name }) }),
  removeFlickrGroupFromList: (listId, name) => apiFetch(`/api/lists/${listId}/flickr-groups/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  addLemmyCommunityToList: (listId, name) => apiFetch(`/api/lists/${listId}/lemmy-communities`, { method: 'POST', body: JSON.stringify({ name }) }),
  removeLemmyCommunityFromList: (listId, name) => apiFetch(`/api/lists/${listId}/lemmy-communities/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  refreshList: (listId) => apiFetch(`/api/lists/${listId}/refresh`, { method: 'POST' }),
  getListStatus: (listId) => apiFetch(`/api/lists/${listId}/status`),
  getSettings: () => apiFetch('/api/config'),
  updateSettings: (data) => apiFetch('/api/config', { method: 'PUT', body: JSON.stringify(data) }),
};

// ── Identifier editors (Subreddits / Flickr Groups / Lemmy Communities) ────
// The three source types share an identical add/remove/list UI, differing
// only in API endpoint, display prefix, and input normalization.
const identifierEditors = [
  {
    source: 'reddit',
    listKey: 'subreddits',
    ulId: 'subreddit-list',
    formId: 'add-subreddit-form',
    inputId: 'subreddit-input',
    displayName: (name) => 'r/' + name,
    normalize: (raw) => raw.trim().toLowerCase().replace(/^r\//, ''),
    add: (listId, name) => api.addSubredditToList(listId, name),
    remove: (listId, name) => api.removeSubredditFromList(listId, name),
    confirmRemove: (name) => `Remove r/${name}? Non-favorited posts from this subreddit will be deleted.`,
  },
  {
    source: 'flickr',
    listKey: 'flickr_groups',
    ulId: 'flickr-group-list',
    formId: 'add-flickr-group-form',
    inputId: 'flickr-group-input',
    displayName: (name) => 'flickr/' + name,
    normalize: (raw) => raw.trim(),
    add: (listId, name) => api.addFlickrGroupToList(listId, name),
    remove: (listId, name) => api.removeFlickrGroupFromList(listId, name),
    confirmRemove: (name) => `Remove Flickr group "${name}"? Non-favorited posts from it will be deleted.`,
  },
  {
    source: 'lemmy',
    listKey: 'lemmy_communities',
    ulId: 'lemmy-community-list',
    formId: 'add-lemmy-community-form',
    inputId: 'lemmy-community-input',
    displayName: (name) => '!' + name,
    normalize: (raw) => raw.trim().toLowerCase().replace(/^!/, ''),
    add: (listId, name) => api.addLemmyCommunityToList(listId, name),
    remove: (listId, name) => api.removeLemmyCommunityFromList(listId, name),
    confirmRemove: (name) => `Remove Lemmy community "!${name}"? Non-favorited posts from it will be deleted.`,
  },
];

// ── Helpers ───────────────────────────────────────────────────────────────
const filteredPosts = () => {
  const posts = state.posts.slice();
  // The API already sorts (favorites first, then newest) but we re-apply here
  // so client-side favorite toggles are reflected instantly without a round-trip.
  posts.sort((a, b) => {
    const af = state.favorites[a.id] ? 1 : 0;
    const bf = state.favorites[b.id] ? 1 : 0;
    if (af !== bf) return bf - af;
    return new Date(b.created_at) - new Date(a.created_at);
  });

  if (state.filter === 'favorites') return posts.filter((p) => state.favorites[p.id]);
  if (state.filter === 'non-favorites') return posts.filter((p) => !state.favorites[p.id]);
  return posts;
};

const isFav = (id) => !!state.favorites[id];

// sourcePrefix renders a post's origin the way each platform conventionally
// refers to it: "r/name" for Reddit, "flickr/name" for a Flickr group, and
// "!name" for a Lemmy community (Lemmy's own community-reference syntax).
const sourcePrefix = (post) => {
  if (post.source === 'flickr') return 'flickr/' + post.subreddit;
  if (post.source === 'lemmy') return '!' + post.subreddit;
  return 'r/' + post.subreddit;
};

// ── View switching ────────────────────────────────────────────────────────
const showView = (view) => {
  document.getElementById('view-home').hidden = view !== 'home';
  document.getElementById('view-settings').hidden = view !== 'settings';
  document.getElementById('view-list').hidden = view !== 'list';
};

const showTab = (tab) => {
  document.querySelectorAll('.tab-btn').forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  document.querySelectorAll('.tab-content').forEach((c) => c.classList.toggle('active', c.id === 'tab-' + tab));
};

// ── Masonry Grid (virtualized) ───────────────────────────────────────────
// With thousands of posts in a list, mounting one DOM node per post (as a
// plain CSS multi-column masonry would) makes every scroll, filter change,
// and favorite toggle rebuild/relayout the entire grid. Instead we compute
// tile positions for *all* filtered posts (cheap arithmetic) but only ever
// mount the handful of tiles near the viewport, adding/removing DOM nodes
// as the user scrolls.
const VIRTUAL_BUFFER_PX = 600;

const MASONRY_BREAKPOINTS = [
  { maxWidth: 380, cols: 1, minColWidth: 140, gap: 6, padding: 6 },
  { maxWidth: 600, cols: 2, minColWidth: 140, gap: 6, padding: 6 },
  { maxWidth: 900, cols: 3, minColWidth: 160, gap: 10, padding: 10 },
  { maxWidth: Infinity, cols: 4, minColWidth: 220, gap: 10, padding: 10 },
];

const masonry = {
  layout: [],          // [{post, index, x, y, w, h}], in filteredPosts() order
  totalHeight: 0,
  mounted: new Map(),  // post.id -> element currently attached to the grid
  footerHeight: null,  // measured once, lazily (fixed by the CSS line-clamp)
  rafPending: false,
  resizeTimer: null,
};

const getMasonryBreakpoint = (width) => MASONRY_BREAKPOINTS.find((b) => width <= b.maxWidth);

// Footer height is effectively fixed (title is line-clamped to 2 lines), but
// we measure it from a live probe rather than hardcoding it so it stays in
// sync with the CSS.
const measureFooterHeight = () => {
  if (masonry.footerHeight != null) return masonry.footerHeight;
  const probe = document.createElement('div');
  probe.className = 'tile-footer';
  probe.style.cssText = 'position:absolute;visibility:hidden;width:220px;left:-9999px;';
  probe.innerHTML = '<span class="tile-sub">r/probe</span><span class="tile-title">Probe title line one probe title line two probe wrap</span>';
  document.body.appendChild(probe);
  masonry.footerHeight = probe.getBoundingClientRect().height || 58;
  document.body.removeChild(probe);
  return masonry.footerHeight;
};

const computeMasonryLayout = (posts) => {
  const grid = document.getElementById('masonry-grid');
  const bp = getMasonryBreakpoint(window.innerWidth);
  const availableWidth = grid.clientWidth - bp.padding * 2;
  const cols = Math.max(1, Math.min(bp.cols, Math.floor((availableWidth + bp.gap) / (bp.minColWidth + bp.gap))));
  const colWidth = (availableWidth - bp.gap * (cols - 1)) / cols;
  const footerHeight = measureFooterHeight();
  const colHeights = new Array(cols).fill(0);
  const layout = [];

  posts.forEach((post, index) => {
    const item = post.media_items[0];
    const ratio = (item.width && item.height) ? item.width / item.height : 1;
    const tileHeight = (colWidth / ratio) + footerHeight + 2; // +2 for the 1px top/bottom border

    let col = 0;
    for (let c = 1; c < cols; c++) {
      if (colHeights[c] < colHeights[col]) col = c;
    }
    const x = bp.padding + col * (colWidth + bp.gap);
    const y = colHeights[col];
    layout.push({ post, index, x, y, w: colWidth, h: tileHeight });
    colHeights[col] = y + tileHeight + bp.gap;
  });

  masonry.layout = layout;
  masonry.totalHeight = colHeights.length ? Math.max(...colHeights) : 0;
};

// Mounts/unmounts tiles based on which fall within the scroll container's
// viewport (plus a buffer), without touching tiles already correctly mounted.
const updateVisibleTiles = () => {
  const container = document.getElementById('tab-media');
  const grid = document.getElementById('masonry-grid');
  if (!container || !grid) return;

  const gridTop = grid.offsetTop;
  const viewTop = container.scrollTop - gridTop - VIRTUAL_BUFFER_PX;
  const viewBottom = container.scrollTop - gridTop + container.clientHeight + VIRTUAL_BUFFER_PX;

  const wanted = new Set();
  masonry.layout.forEach((entry) => {
    if (entry.y + entry.h < viewTop || entry.y > viewBottom) return;
    wanted.add(entry.post.id);
    if (!masonry.mounted.has(entry.post.id)) {
      const el = buildTile(entry.post, entry.index);
      el.style.setProperty('--tx', `${entry.x}px`);
      el.style.setProperty('--ty', `${entry.y}px`);
      el.style.width = `${entry.w}px`;
      grid.appendChild(el);
      masonry.mounted.set(entry.post.id, el);
    }
  });

  masonry.mounted.forEach((el, id) => {
    if (!wanted.has(id)) {
      el.remove();
      masonry.mounted.delete(id);
    }
  });
};

const renderGrid = () => {
  const grid = document.getElementById('masonry-grid');
  const empty = document.getElementById('media-empty');
  const loading = document.getElementById('media-loading');

  loading.hidden = true;
  const posts = filteredPosts();

  document.getElementById('post-count').textContent = `${posts.length} post${posts.length !== 1 ? 's' : ''}`;

  // A fresh layout invalidates every tile's position, so start clean; the
  // set of *mounted* tiles stays small regardless of list size.
  masonry.mounted.forEach((el) => el.remove());
  masonry.mounted.clear();

  if (posts.length === 0) {
    grid.style.height = '';
    empty.hidden = false;
    return;
  }

  empty.hidden = true;

  // Skip layout math while the media tab isn't visible (clientWidth would
  // read 0); the tab-switch handler re-renders when it becomes visible again.
  if (!document.getElementById('tab-media').classList.contains('active')) return;

  computeMasonryLayout(posts);
  grid.style.height = `${masonry.totalHeight}px`;
  updateVisibleTiles();
};

const buildTile = (post, postIndex) => {
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
  sub.textContent = sourcePrefix(post);
  const title = document.createElement('span');
  title.className = 'tile-title';
  title.textContent = post.title;
  footer.appendChild(sub);
  footer.appendChild(title);
  div.appendChild(footer);

  // Open viewer on click
  div.addEventListener('click', () => openViewer(postIndex));

  return div;
};

// ── Favorite toggling ─────────────────────────────────────────────────────
const handleToggleFavorite = async (postId, starEl) => {
  // Optimistic update
  const wasF = isFav(postId);
  state.favorites[postId] = !wasF;
  // Always re-render: favorites sort first, so any toggle shifts tile indices.
  renderGrid();
  updateStarEls(postId);

  try {
    const data = await api.toggleFavorite(state.currentListId, postId);
    state.favorites[postId] = data.favorited;
  } catch (e) {
    // Revert on error
    state.favorites[postId] = wasF;
    console.error('Favorite toggle failed:', e);
  }
  renderGrid();
  updateStarEls(postId);
};

const updateStarEls = (postId) => {
  const fav = isFav(postId);
  // Grid tiles
  document.querySelectorAll(`[data-post-id="${postId}"] .tile-star`).forEach((el) => {
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
};

// ── Viewer ────────────────────────────────────────────────────────────────
const openViewer = (postIndex, mediaIndex = 0) => {
  const posts = filteredPosts();
  if (postIndex < 0 || postIndex >= posts.length) return;

  state.viewer.active = true;
  state.viewer.postIndex = postIndex;
  state.viewer.mediaIndex = mediaIndex;

  const viewer = document.getElementById('viewer');
  viewer.hidden = false;
  document.body.style.overflow = 'hidden';

  renderViewerContent();
};

const closeViewer = () => {
  state.viewer.active = false;
  document.getElementById('viewer').hidden = true;
  document.body.style.overflow = '';
  stopAllViewerMedia();
};

const renderViewerContent = () => {
  const posts = filteredPosts();
  const post = posts[state.viewer.postIndex];
  if (!post) { closeViewer(); return; }

  const mi = state.viewer.mediaIndex;
  const item = post.media_items[mi] || post.media_items[0];
  const isMulti = post.media_items.length > 1;

  // Meta
  document.getElementById('viewer-subreddit').textContent = sourcePrefix(post);
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
    video.muted = !state.videoUnmuted;
    video.loop = true;
    video.controls = true;
    video.playsInline = true;
    video.style.maxWidth = '100%';
    video.style.maxHeight = '100%';
    video.addEventListener('volumechange', () => {
      state.videoUnmuted = !video.muted;
    });
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
};

const stopAllViewerMedia = () => {
  const mediaDiv = document.getElementById('viewer-media');
  mediaDiv.querySelectorAll('video').forEach((v) => { v.pause(); v.src = ''; });
};

const renderDots = (post, currentIndex) => {
  const dotsDiv = document.getElementById('viewer-dots');
  dotsDiv.innerHTML = '';
  if (post.media_items.length <= 1) return;

  post.media_items.forEach((_, i) => {
    const dot = document.createElement('div');
    dot.className = 'dot' + (i === currentIndex ? ' active' : '');
    dot.addEventListener('click', () => navigateMedia(i - state.viewer.mediaIndex));
    dotsDiv.appendChild(dot);
  });
};

const navigateMedia = (delta) => {
  const posts = filteredPosts();
  const post = posts[state.viewer.postIndex];
  if (!post) return;

  const newIndex = state.viewer.mediaIndex + delta;
  if (newIndex < 0 || newIndex >= post.media_items.length) return;
  state.viewer.mediaIndex = newIndex;
  renderViewerContent();
};

const navigatePost = (delta) => {
  const posts = filteredPosts();
  const newIndex = state.viewer.postIndex + delta;
  if (newIndex < 0 || newIndex >= posts.length) return;
  state.viewer.postIndex = newIndex;
  state.viewer.mediaIndex = 0;
  renderViewerContent();
};

// ── Swipe / touch handling ────────────────────────────────────────────────
let touchStart = null;

const setupSwipe = (el) => {
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
};

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

// ── Home view (list of curation lists) ───────────────────────────────────
const buildListCard = (list) => {
  const card = document.createElement('div');
  card.className = 'list-card';

  const body = document.createElement('div');
  body.className = 'list-card-body';
  body.addEventListener('click', () => navigate(`#/list/${list.id}`));

  const name = document.createElement('div');
  name.className = 'list-card-name';
  name.textContent = list.name;

  const meta = document.createElement('div');
  meta.className = 'list-card-meta';
  const subCount = list.subreddits.length;
  meta.textContent = `${subCount} subreddit${subCount !== 1 ? 's' : ''} · ${list.post_count} post${list.post_count !== 1 ? 's' : ''} · ${list.favorite_count} ★`;

  body.appendChild(name);
  body.appendChild(meta);

  const actions = document.createElement('div');
  actions.className = 'list-card-actions';

  const renameBtn = document.createElement('button');
  renameBtn.className = 'btn-icon-sm';
  renameBtn.textContent = 'Rename';
  renameBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    handleRenameList(list.id, list.name);
  });

  const deleteBtn = document.createElement('button');
  deleteBtn.className = 'btn-icon-sm danger';
  deleteBtn.textContent = 'Delete';
  deleteBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    handleDeleteList(list.id, list.name);
  });

  actions.appendChild(renameBtn);
  actions.appendChild(deleteBtn);

  card.appendChild(body);
  card.appendChild(actions);
  return card;
};

const renderHome = () => {
  const grid = document.getElementById('list-cards');
  const empty = document.getElementById('lists-empty');
  grid.innerHTML = '';

  if (state.lists.length === 0) {
    empty.hidden = false;
    return;
  }

  empty.hidden = true;
  state.lists.forEach((list) => grid.appendChild(buildListCard(list)));
};

const handleCreateList = async (name, subredditsRaw) => {
  const subreddits = subredditsRaw
    .split(/[,\n]/)
    .map((s) => s.trim().toLowerCase().replace(/^r\//, ''))
    .filter(Boolean);

  await api.createList(name, subreddits);
  await showHome();
};

const handleRenameList = async (listId, currentName) => {
  const name = prompt('Rename list', currentName);
  if (!name || !name.trim() || name.trim() === currentName) return;
  try {
    await api.renameList(listId, name.trim());
    await showHome();
  } catch (e) {
    alert('Failed to rename list: ' + e.message);
  }
};

const handleDeleteList = async (listId, name) => {
  if (!confirm(`Delete "${name}"? ALL posts and favorites in this list will be permanently deleted. This cannot be undone.`)) return;
  try {
    await api.deleteList(listId);
    await showHome();
  } catch (e) {
    alert('Failed to delete list: ' + e.message);
  }
};

// ── List view: Config tab (subreddits / flickr groups / lemmy communities) ─
const renderIdentifierList = (editor) => {
  const ul = document.getElementById(editor.ulId);
  const names = state.currentList?.[editor.listKey] || [];

  if (names.length === 0) {
    ul.innerHTML = '<li style="color:var(--text-muted);font-size:13px;padding:8px 0">None yet.</li>';
    return;
  }

  ul.innerHTML = '';
  names.forEach((name) => {
    const li = document.createElement('li');
    li.className = 'subreddit-item';

    const left = document.createElement('div');
    const nameEl = document.createElement('span');
    nameEl.className = 'subreddit-name';
    nameEl.textContent = editor.displayName(name);

    const lastChecked = state.status?.last_checked?.[editor.source]?.[name];
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
    delBtn.addEventListener('click', () => handleRemoveIdentifier(editor, name));
    actions.appendChild(delBtn);

    li.appendChild(left);
    li.appendChild(actions);
    ul.appendChild(li);
  });
};

const renderAllIdentifierLists = () => identifierEditors.forEach(renderIdentifierList);

const handleRemoveIdentifier = async (editor, name) => {
  if (!confirm(editor.confirmRemove(name))) return;
  try {
    await editor.remove(state.currentListId, name);
    await loadListView();
    renderGrid();
    renderAllIdentifierLists();
  } catch (e) {
    alert('Failed to remove: ' + e.message);
  }
};

const renderStatusPanel = () => {
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

  identifierEditors.forEach((editor) => {
    const names = state.currentList?.[editor.listKey] || [];
    if (names.length === 0) return;

    const hdr = document.createElement('div');
    hdr.className = 'status-label';
    hdr.style.marginTop = '8px';
    hdr.textContent = editor.source[0].toUpperCase() + editor.source.slice(1) + ' last checked:';
    panel.appendChild(hdr);

    const subList = document.createElement('div');
    subList.className = 'status-sub-list';
    names.forEach((name) => {
      const t = s.last_checked?.[editor.source]?.[name];
      const row = document.createElement('div');
      row.className = 'status-sub-row';
      row.innerHTML = `<span>${editor.displayName(name)}</span><span style="color:var(--text-muted)">${!t || t === 'never' ? 'never' : new Date(t).toLocaleString()}</span>`;
      subList.appendChild(row);
    });
    panel.appendChild(subList);
  });
};

// ── Settings view (global) ────────────────────────────────────────────────
const populateSettingsForm = () => {
  if (!state.settings) return;
  document.getElementById('cfg-interval').value = state.settings.check_interval || '';
  document.getElementById('cfg-download-dir').value = state.settings.download_dir || '';
  document.getElementById('cfg-max-age').value = state.settings.max_post_age_days ?? '';
  document.getElementById('cfg-imgur-client-id').value = state.settings.imgur_client_id || '';
  document.getElementById('cfg-flickr-api-key').value = state.settings.flickr_api_key || '';
};

// ── Data loading ──────────────────────────────────────────────────────────
const loadPosts = async () => {
  const data = await api.getPosts(state.currentListId);
  state.posts = data || [];
  state.favorites = {};
  state.posts.forEach((p) => { if (p.favorited) state.favorites[p.id] = true; });
};

const loadListView = async () => {
  const [list, status] = await Promise.all([
    api.getList(state.currentListId),
    api.getListStatus(state.currentListId).catch(() => null),
  ]);
  state.currentList = list;
  state.status = status;
  await loadPosts();
};

const showHome = async () => {
  state.view = 'home';
  showView('home');
  document.getElementById('view-home').scrollTop = 0;
  try {
    state.lists = await api.getLists();
  } catch (e) {
    console.error('Failed to load lists:', e);
    state.lists = [];
  }
  renderHome();
};

const showSettings = async () => {
  state.view = 'settings';
  showView('settings');
  try {
    state.settings = await api.getSettings();
    populateSettingsForm();
  } catch (e) {
    console.error('Failed to load settings:', e);
  }
};

const openList = async (listId) => {
  state.view = 'list';
  state.currentListId = listId;
  state.filter = 'all';
  document.querySelectorAll('.filter-btn').forEach((b) => b.classList.toggle('active', b.dataset.filter === 'all'));
  showTab('media');
  showView('list');
  document.getElementById('tab-media').scrollTop = 0;

  document.getElementById('media-loading').hidden = false;

  try {
    await loadListView();
  } catch (e) {
    console.error('Failed to load list:', e);
  }

  document.getElementById('current-list-name').textContent = state.currentList ? state.currentList.name : '';
  renderGrid();
  renderAllIdentifierLists();
  renderStatusPanel();
};

// ── Hash router ───────────────────────────────────────────────────────────
const route = () => {
  const hash = location.hash.slice(1); // strip leading '#'
  if (hash.startsWith('/list/')) {
    openList(hash.slice('/list/'.length));
    return;
  }
  if (hash === '/settings') {
    showSettings();
    return;
  }
  showHome();
};

const navigate = (hash) => {
  if (location.hash === hash) {
    route();
    return;
  }
  location.hash = hash;
};

const setupIdentifierEditorEvents = () => {
  identifierEditors.forEach((editor) => {
    document.getElementById(editor.formId).addEventListener('submit', async (e) => {
      e.preventDefault();
      const input = document.getElementById(editor.inputId);
      const name = editor.normalize(input.value);
      if (!name) return;
      try {
        await editor.add(state.currentListId, name);
        input.value = '';
        await loadListView();
        renderGrid();
        renderAllIdentifierLists();
        renderStatusPanel();
      } catch (err) {
        alert('Failed to add: ' + err.message);
      }
    });
  });
};

// ── Event wiring ──────────────────────────────────────────────────────────
const setupEvents = () => {
  document.getElementById('settings-btn').addEventListener('click', () => navigate('#/settings'));
  document.getElementById('back-from-settings-btn').addEventListener('click', () => navigate('#/'));
  document.getElementById('back-to-home-btn').addEventListener('click', () => navigate('#/'));

  document.getElementById('new-list-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const nameInput = document.getElementById('new-list-name');
    const subsInput = document.getElementById('new-list-subreddits');
    const name = nameInput.value.trim();
    if (!name) return;
    try {
      await handleCreateList(name, subsInput.value);
      nameInput.value = '';
      subsInput.value = '';
    } catch (err) {
      alert('Failed to create list: ' + err.message);
    }
  });

  // Tab switching (within a list)
  document.querySelectorAll('.tab-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      showTab(btn.dataset.tab);
      if (btn.dataset.tab === 'config') {
        renderAllIdentifierLists();
        renderStatusPanel();
      } else if (btn.dataset.tab === 'media') {
        // Re-render in case the window was resized while this tab was hidden
        // (clientWidth reads 0 while display:none, so layout is skipped then).
        renderGrid();
      }
    });
  });

  // Masonry virtualization: mount/unmount tiles as the grid scrolls, and
  // recompute the layout (column count, tile sizes) when the viewport resizes.
  document.getElementById('tab-media').addEventListener('scroll', () => {
    if (masonry.rafPending) return;
    masonry.rafPending = true;
    requestAnimationFrame(() => {
      masonry.rafPending = false;
      updateVisibleTiles();
    });
  }, { passive: true });

  window.addEventListener('resize', () => {
    clearTimeout(masonry.resizeTimer);
    masonry.resizeTimer = setTimeout(() => {
      if (state.view === 'list') renderGrid();
    }, 150);
  });

  // Filter buttons
  document.querySelectorAll('.filter-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      state.filter = btn.dataset.filter;
      document.querySelectorAll('.filter-btn').forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');
      renderGrid();
    });
  });

  // Refresh button in toolbar
  const refreshBtn = document.getElementById('refresh-btn');
  refreshBtn.addEventListener('click', async () => {
    refreshBtn.classList.add('spinning');
    try {
      await api.refreshList(state.currentListId);
      // Give the scheduler a moment to fetch then reload.
      await new Promise((r) => setTimeout(r, 3000));
      await loadListView();
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

  // ── Config events (per list) ──
  setupIdentifierEditorEvents();

  // ── Settings events (global) ──
  document.getElementById('settings-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const saved = document.getElementById('settings-saved');
    try {
      await api.updateSettings({
        check_interval:    document.getElementById('cfg-interval').value.trim(),
        download_dir:      document.getElementById('cfg-download-dir').value.trim(),
        max_post_age_days: parseInt(document.getElementById('cfg-max-age').value, 10) || 30,
        imgur_client_id:   document.getElementById('cfg-imgur-client-id').value.trim(),
        flickr_api_key:    document.getElementById('cfg-flickr-api-key').value.trim(),
      });
      state.settings = await api.getSettings();
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
      await api.refreshList(state.currentListId);
      await new Promise((r) => setTimeout(r, 4000));
      await loadListView();
      renderGrid();
      renderAllIdentifierLists();
      renderStatusPanel();
    } catch (e) {
      console.error('Refresh failed:', e);
    } finally {
      btn.textContent = 'Check subreddits now';
      btn.disabled = false;
    }
  });

  window.addEventListener('hashchange', route);
};

// ── Bootstrap ─────────────────────────────────────────────────────────────
const init = () => {
  setupEvents();
  route();
};

init();
