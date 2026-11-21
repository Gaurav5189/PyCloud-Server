/**
 * PyCloud v2.0 — client engine
 *
 * Security notes:
 *  - Every value that comes from the server or the file system is rendered with
 *    textContent / createElement. There is no innerHTML anywhere in this file.
 *  - Mutating requests attach the CSRF token from the pycloud_csrf cookie.
 *  - The page works without a build step and without third-party scripts.
 */

(function () {
    'use strict';

    /* ── Small DOM helpers ─────────────────────────────────────────────── */

    const SVG_NS = 'http://www.w3.org/2000/svg';
    const $ = (selector, root) => (root || document).querySelector(selector);
    const $$ = (selector, root) => Array.from((root || document).querySelectorAll(selector));

    function el(tag, className, text) {
        const node = document.createElement(tag);
        if (className) node.className = className;
        if (text !== undefined && text !== null) node.textContent = String(text);
        return node;
    }

    function icon(name, className) {
        const svg = document.createElementNS(SVG_NS, 'svg');
        svg.setAttribute('class', 'icon' + (className ? ' ' + className : ''));
        svg.setAttribute('aria-hidden', 'true');
        svg.setAttribute('focusable', 'false');
        const use = document.createElementNS(SVG_NS, 'use');
        use.setAttribute('href', '#i-' + name);
        svg.appendChild(use);
        return svg;
    }

    function clear(node) {
        if (node) node.textContent = '';
    }

    function setText(id, value) {
        const node = document.getElementById(id);
        if (node) node.textContent = value;
    }

    function readPref(key, fallback) {
        try {
            const value = localStorage.getItem(key);
            return value === null ? fallback : value;
        } catch (err) {
            return fallback;
        }
    }

    function writePref(key, value) {
        try {
            localStorage.setItem(key, value);
        } catch (err) {
            /* storage can be disabled — preferences simply do not persist */
        }
    }

    function debounce(fn, wait) {
        let timer = null;
        return function () {
            const args = arguments;
            clearTimeout(timer);
            timer = setTimeout(() => fn.apply(null, args), wait);
        };
    }

    function getCSRFToken() {
        const match = document.cookie.match(/(^|;)\s*pycloud_csrf=([^;]+)/);
        return match ? decodeURIComponent(match[2]) : '';
    }

    async function apiFetch(url, options) {
        const opts = Object.assign({}, options);
        opts.headers = Object.assign({}, opts.headers);
        if (opts.cache === undefined) opts.cache = 'no-store';
        const method = (opts.method || 'GET').toUpperCase();
        if (method !== 'GET' && method !== 'HEAD') {
            const csrf = getCSRFToken();
            if (csrf) opts.headers['X-CSRF-Token'] = csrf;
        }

        let res;
        try {
            res = await fetch(url, opts);
        } catch (err) {
            // A rejected fetch is the only real signal that the server is
            // unreachable. An abort is a deliberate cancellation, not an outage.
            if (!(err && err.name === 'AbortError')) noteRequestFailure();
            throw err;
        }

        // Any HTTP reply — even 4xx — proves the server is up. Only gateway
        // errors mean the upstream is actually down.
        if (res.status === 502 || res.status === 503 || res.status === 504) noteRequestFailure();
        else noteRequestSuccess();

        const isAuthEndpoint = url.indexOf('/api/login') === 0 ||
            url.indexOf('/api/setup') === 0 ||
            url.indexOf('/api/status') === 0 ||
            url.indexOf('/api/logout') === 0;

        if (!isAuthEndpoint) {
            if (res.status === 401) {
                showLogin();
                const error = new Error('Authentication required');
                error.code = 401;
                throw error;
            }
            if (res.status === 428) {
                showSetup();
                const error = new Error('Initial setup required');
                error.code = 428;
                throw error;
            }
        }
        return res;
    }

    async function readError(res, fallback) {
        try {
            const data = await res.json();
            return data && data.error ? data.error : fallback;
        } catch (err) {
            return fallback;
        }
    }

    async function fetchDir(path) {
        const res = await apiFetch('/api/files?path=' + encodeURIComponent(path));
        if (!res.ok) {
            throw new Error(await readError(res, 'Could not open this folder'));
        }
        return res.json();
    }

    /* ── Formatting ────────────────────────────────────────────────────── */

    function formatBytes(bytes) {
        const value = Number(bytes);
        if (!Number.isFinite(value) || value <= 0) return '0 B';
        const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)));
        const amount = value / Math.pow(1024, index);
        const digits = index === 0 ? 0 : amount >= 100 ? 0 : 1;
        return amount.toFixed(digits) + ' ' + units[index];
    }

    function parseMetric(value) {
        if (value === null || value === undefined || value === 'N/A') return null;
        const number = parseFloat(String(value).replace('%', '').trim());
        return Number.isFinite(number) ? number : null;
    }

    function formatDate(timestamp) {
        if (!timestamp) return '—';
        const date = new Date(timestamp * 1000);
        if (Number.isNaN(date.getTime())) return '—';
        const now = new Date();
        const sameYear = date.getFullYear() === now.getFullYear();
        return date.toLocaleString(undefined, sameYear
            ? { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }
            : { day: '2-digit', month: 'short', year: 'numeric' });
    }

    function timeAgo(timestamp) {
        if (!timestamp) return '—';
        const seconds = Math.max(0, Math.floor(Date.now() / 1000 - timestamp));
        if (seconds < 60) return seconds + 's ago';
        const minutes = Math.floor(seconds / 60);
        if (minutes < 60) return minutes + 'm ago';
        const hours = Math.floor(minutes / 60);
        if (hours < 24) return hours + 'h ago';
        return Math.floor(hours / 24) + 'd ago';
    }

    function formatDuration(totalSeconds) {
        const seconds = Math.max(0, Math.floor(totalSeconds));
        const hours = Math.floor(seconds / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        const secs = seconds % 60;
        const pad = (n) => String(n).padStart(2, '0');
        return hours > 0 ? hours + ':' + pad(minutes) + ':' + pad(secs) : pad(minutes) + ':' + pad(secs);
    }

    function formatUptimeLong(totalSeconds) {
        const seconds = Math.max(0, Math.floor(totalSeconds));
        const hours = Math.floor(seconds / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        const secs = seconds % 60;
        if (hours > 0) return hours + 'h ' + minutes + 'm';
        if (minutes > 0) return minutes + 'm ' + secs + 's';
        return secs + 's';
    }

    function formatUptimeCompact(totalSeconds) {
        const seconds = Math.max(0, Math.floor(totalSeconds));
        const days = Math.floor(seconds / 86400);
        const hours = Math.floor((seconds % 86400) / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        if (days > 0) return days + 'd ' + hours + 'h';
        if (hours > 0) return hours + 'h ' + minutes + 'm';
        if (minutes > 0) return minutes + 'm';
        return seconds + 's';
    }

    // The server reports its own uptime; we anchor that value to a local
    // timestamp so the clock can tick locally between polls. Two browser tabs
    // therefore show the same figure, and a server restart shows up on the next
    // poll instead of resetting per page load.
    function setServerUptime(seconds, startedAt) {
        const value = Number(seconds);
        if (Number.isFinite(value)) {
            state.serverUptimeBase = value;
            state.serverUptimeAt = Date.now();
        }
        const started = Number(startedAt);
        if (Number.isFinite(started) && started > 0) state.serverStartedAt = started;
    }

    function serverUptimeNow() {
        if (state.serverUptimeBase === null) return null;
        return Math.max(0, Math.floor(state.serverUptimeBase + (Date.now() - state.serverUptimeAt) / 1000));
    }

    function formatServerStart() {
        if (!state.serverStartedAt) return '—';
        const date = new Date(state.serverStartedAt * 1000);
        if (Number.isNaN(date.getTime())) return '—';
        return date.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
    }

    /* ── State ─────────────────────────────────────────────────────────── */

    const state = {
        view: 'overview',
        authenticated: false,
        status: null,
        path: '',
        category: 'all',
        files: [],
        filteredFiles: [],
        filesLoaded: false,
        filesToken: 0,
        filesAt: 0,
        rootCount: null,
        rootOtherSize: 0,
        rootOtherSizeAt: 0,
        sort: 'recent',
        tasks: [],
        tasksFilter: 'all',
        tasksHash: '',
        taskStatus: {},
        openLogIds: new Set(),
        mediaFiles: [],
        mediaFilter: 'all',
        mediaLoaded: false,
        mediaLoading: false,
        mediaAt: 0,
        libraryCounts: { uploads: null, youtube: null, trash: null },
        librarySizes: { uploads: 0, youtube: 0, trash: 0 },
        libraryAt: 0,
        stats: null,
        statsHistory: { cpu: [], ram: [] },
        themePref: readPref('pycloud.theme', 'graphite'),
        motionPref: readPref('pycloud.motion', 'full'),
        activeUploadXHR: null,
        confirmResolve: null,
        confirmExpected: '',
        overlayStack: [],
        palette: { items: [], index: 0, query: '', open: false },
        connFails: 0,
        connected: true,
        serverUptimeBase: null,
        serverUptimeAt: 0,
        serverStartedAt: null,
        recentFiles: [],
        pollStats: null,
        pollTasks: null,
        thumbObserver: null,
        fetchBusy: false,
        overviewToken: 0
    };

    /* ── Toasts ────────────────────────────────────────────────────────── */

    function toast(message, type, action) {
        const container = $('#toastContainer');
        if (!container) return;
        const kind = type === 'success' || type === 'error' ? type : 'info';
        const node = el('div', 'toast toast-' + kind);
        node.setAttribute('role', kind === 'error' ? 'alert' : 'status');

        const iconWrap = el('span', 'toast-ico');
        iconWrap.appendChild(icon(kind === 'success' ? 'check' : kind === 'error' ? 'alert' : 'info'));

        const text = el('span', 'toast-msg', message);
        node.appendChild(iconWrap);
        node.appendChild(text);

        let timer = null;
        const dismiss = () => {
            if (!node.isConnected) return;
            clearTimeout(timer);
            node.classList.add('is-leaving');
            setTimeout(() => node.remove(), 200);
        };

        if (action && action.label && typeof action.onClick === 'function') {
            const button = el('button', 'toast-action', action.label);
            button.type = 'button';
            button.addEventListener('click', () => {
                dismiss();
                action.onClick();
            });
            node.appendChild(button);
        } else {
            const close = el('button', 'toast-close');
            close.type = 'button';
            close.setAttribute('aria-label', 'Dismiss notification');
            close.appendChild(icon('x'));
            close.addEventListener('click', dismiss);
            node.appendChild(close);
        }

        container.appendChild(node);
        while (container.children.length > 4) {
            container.removeChild(container.firstElementChild);
        }
        timer = setTimeout(dismiss, action ? 7000 : 4500);
        node.addEventListener('mouseenter', () => clearTimeout(timer));
        node.addEventListener('mouseleave', () => { timer = setTimeout(dismiss, 2500); });
    }

    /* ── Overlay manager ───────────────────────────────────────────────── */

    function openOverlay(id, options) {
        const overlay = document.getElementById(id);
        if (!overlay || !overlay.hidden) return;
        const opts = options || {};
        overlay._lastFocus = document.activeElement;
        overlay.hidden = false;
        state.overlayStack = state.overlayStack.filter((entry) => entry !== id);
        state.overlayStack.push(id);
        document.body.classList.add('modal-open');

        const focusTarget = typeof opts.focus === 'string'
            ? overlay.querySelector(opts.focus)
            : opts.focus || overlay.querySelector('input:not([type="hidden"]), button, [href]');
        if (focusTarget) {
            setTimeout(() => {
                if (!overlay.hidden && typeof focusTarget.focus === 'function') focusTarget.focus();
            }, 30);
        }
    }

    function closeOverlay(id) {
        const overlay = document.getElementById(id);
        if (!overlay || overlay.hidden) return;
        overlay.hidden = true;
        state.overlayStack = state.overlayStack.filter((entry) => entry !== id);
        if (state.overlayStack.length === 0) {
            document.body.classList.remove('modal-open');
        }

        if (id === 'playerModal') {
            const stage = $('#cinemaContent');
            if (stage) {
                $$('video, audio', stage).forEach((node) => {
                    try {
                        node.pause();
                        node.removeAttribute('src');
                        node.load();
                    } catch (err) { /* ignore */ }
                });
                stage.textContent = '';
            }
        }
        if (id === 'paletteModal') {
            state.palette.open = false;
        }
        if (id === 'confirmModal' && state.confirmResolve) {
            const resolve = state.confirmResolve;
            state.confirmResolve = null;
            resolve(false);
        }

        const last = overlay._lastFocus;
        if (last && document.contains(last) && typeof last.focus === 'function') {
            try { last.focus(); } catch (err) { /* ignore detached focus */ }
        }
    }

    function closeTopOverlay() {
        const id = state.overlayStack[state.overlayStack.length - 1];
        if (!id) return false;
        const overlay = document.getElementById(id);
        if (!overlay) {
            state.overlayStack.pop();
            return false;
        }
        if (overlay.classList.contains('auth-overlay')) return false;
        closeOverlay(id);
        return true;
    }

    function trapFocus(event) {
        const id = state.overlayStack[state.overlayStack.length - 1];
        if (!id) return;
        const overlay = document.getElementById(id);
        if (!overlay) return;
        const focusable = $$('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])', overlay)
            .filter((node) => node.offsetParent !== null || node === document.activeElement);
        if (focusable.length === 0) {
            event.preventDefault();
            return;
        }
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
        }
    }

    function confirmAction(options) {
        return new Promise((resolve) => {
            state.confirmResolve = resolve;
            setText('confirmTitle', options.title || 'Confirm');
            setText('confirmMessage', options.message || '');
            const ok = $('#btnConfirmOk');
            const cancel = $('#btnConfirmCancel');
            const wrap = $('#confirmInputWrap');
            const input = $('#confirmInput');
            const expected = options.requireText || '';
            state.confirmExpected = expected;

            if (ok) {
                ok.textContent = options.confirmLabel || 'Confirm';
                ok.className = options.danger === false ? 'btn btn-primary' : 'btn btn-danger';
            }
            if (cancel) cancel.textContent = options.cancelLabel || 'Cancel';
            if (wrap) wrap.hidden = !expected;
            if (input) input.value = '';
            if (ok) ok.disabled = Boolean(expected);
            if (expected) setText('confirmExpected', expected);

            openOverlay('confirmModal', { focus: expected ? input : ok });
        });
    }

    function settleConfirm(value) {
        const resolve = state.confirmResolve;
        state.confirmResolve = null;
        state.confirmExpected = '';
        closeOverlay('confirmModal');
        if (resolve) resolve(value);
    }

    /* ── Theme + motion ────────────────────────────────────────────────── */

    function resolveTheme(pref) {
        if (pref === 'system') {
            return window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches
                ? 'paper'
                : 'graphite';
        }
        return pref === 'paper' || pref === 'terminal' ? pref : 'graphite';
    }

    function applyTheme(pref, persist) {
        const allowed = ['graphite', 'paper', 'terminal', 'system'];
        const choice = allowed.indexOf(pref) >= 0 ? pref : 'graphite';
        const resolved = resolveTheme(choice);
        const root = document.documentElement;
        state.themePref = choice;
        root.setAttribute('data-theme', resolved);
        root.setAttribute('data-theme-pref', choice);

        const meta = document.querySelector('meta[name="theme-color"]');
        if (meta) {
            const value = getComputedStyle(root).getPropertyValue('--theme-color').trim();
            if (value) meta.setAttribute('content', value);
        }

        $$('#themePicker [data-theme-opt]').forEach((button) => {
            button.setAttribute('aria-pressed', button.dataset.themeOpt === choice ? 'true' : 'false');
        });

        const toggle = $('#btnTheme');
        if (toggle) {
            toggle.title = resolved === 'paper' ? 'Switch to dark theme' : 'Switch to light theme';
            toggle.setAttribute('aria-label', toggle.title);
        }
        if (persist) writePref('pycloud.theme', choice);
    }

    function cycleTheme() {
        applyTheme(resolveTheme(state.themePref) === 'paper' ? 'graphite' : 'paper', true);
    }

    function applyMotion(pref, persist) {
        const choice = pref === 'reduced' ? 'reduced' : 'full';
        state.motionPref = choice;
        document.documentElement.setAttribute('data-motion', choice);
        const checkbox = $('#prefMotion');
        if (checkbox) checkbox.checked = choice === 'reduced';
        if (persist) writePref('pycloud.motion', choice);
    }

    /* ── Navigation ────────────────────────────────────────────────────── */

    const VIEW_TITLES = {
        overview: 'Overview',
        files: 'Files',
        media: 'Media',
        tasks: 'Tasks',
        settings: 'Settings'
    };

    function currentHashView() {
        const view = (location.hash || '').replace('#', '');
        return VIEW_TITLES[view] ? view : 'overview';
    }

    function go(view) {
        const target = VIEW_TITLES[view] ? view : 'overview';
        if (location.hash === '#' + target) {
            renderView(target);
        } else {
            location.hash = target;
        }
    }

    function renderView(view) {
        const target = VIEW_TITLES[view] ? view : 'overview';
        if (state.view === 'media' && target !== 'media') {
            releaseMediaVideos();
        }
        state.view = target;
        $$('.view').forEach((section) => {
            section.hidden = section.dataset.view !== target;
        });
        $$('.nav-item').forEach((link) => {
            const active = link.dataset.nav === target;
            link.classList.toggle('is-active', active);
            if (active) link.setAttribute('aria-current', 'page');
            else link.removeAttribute('aria-current');
        });
        setText('viewTitle', VIEW_TITLES[target]);
        updateViewSub();
        closeSidebar();
        onViewEnter(target);
    }

    function updateViewSub() {
        if (state.view === 'overview') {
            setText('viewSub', 'System overview');
        } else if (state.view === 'files') {
            setText('viewSub', state.path ? '/' + state.path : 'Root directory');
        } else if (state.view === 'media') {
            const counts = mediaCounts();
            setText('viewSub', counts.total ? counts.video + ' video · ' + counts.audio + ' audio · ' + counts.image + ' image' : 'Media libraries');
        } else if (state.view === 'tasks') {
            const active = activeTaskCount();
            setText('viewSub', active + ' active · ' + state.tasks.length + ' total');
        } else if (state.view === 'settings') {
            setText('viewSub', 'PyCloud ' + (state.status && state.status.version ? 'v' + state.status.version : 'v2.0'));
        }
    }

    function onViewEnter(view) {
        if (view === 'overview') {
            loadOverview(false);
        } else if (view === 'files') {
            loadLibraries(false);
            if (!state.filesLoaded) {
                loadFiles(state.path || '');
            } else if (Date.now() - state.filesAt > 10000) {
                loadFiles(state.path || '', { silent: true });
            } else {
                updateUploadTarget();
            }
        } else if (view === 'media') {
            loadMedia(false);
        } else if (view === 'tasks') {
            updateTasks(true);
        } else if (view === 'settings') {
            renderSettings();
        }
    }

    function openLibrary(name) {
        const path = LIBRARY_PATHS[name];
        if (!path) return;
        state.category = name;
        state.path = path;
        state.filesLoaded = false;
        go('files');
    }

    function openSidebar() {
        document.body.classList.add('sidebar-open');
        const scrim = $('#sidebarScrim');
        if (scrim) scrim.hidden = false;
        const menu = $('#btnMenu');
        if (menu) menu.setAttribute('aria-expanded', 'true');
    }

    function closeSidebar() {
        document.body.classList.remove('sidebar-open');
        const scrim = $('#sidebarScrim');
        if (scrim) scrim.hidden = true;
        const menu = $('#btnMenu');
        if (menu) menu.setAttribute('aria-expanded', 'false');
    }

    /* ── Server status / auth ──────────────────────────────────────────── */

    function updateServerChrome() {
        const version = state.status && state.status.version ? state.status.version : '2.0.0';
        const tls = Boolean(state.status && state.status.tls_enabled);
        setText('serverEndpoint', location.host || 'localhost');
        setText('serverVersion', 'v' + version);
        setText('serverProto', tls ? 'HTTPS' : 'HTTP');
        setText('authVersion', 'v' + version);
        setText('setEndpoint', location.host || 'localhost');
        setText('setVersion', 'v' + version);
        setText('setProtocol', (location.protocol || 'http:').replace(':', '').toUpperCase() + (tls ? ' · TLS' : ' · plain'));
        setText('setSession', formatServerStart());
    }

    function showLogin() {
        state.authenticated = false;
        const setup = $('#setupModal');
        if (setup) setup.hidden = true;
        const feedback = $('#loginFeedback');
        if (feedback) feedback.textContent = '';
        openOverlay('loginModal', { focus: '#loginPassword' });
    }

    function showSetup() {
        state.authenticated = false;
        const login = $('#loginModal');
        if (login) login.hidden = true;
        const feedback = $('#setupFeedback');
        if (feedback) feedback.textContent = '';
        openOverlay('setupModal', { focus: '#setupPassword' });
    }

    async function checkServerStatus() {
        try {
            const res = await fetch('/api/status', { cache: 'no-store' });
            const data = await res.json();
            state.status = data;
            setServerUptime(data.uptime_seconds, data.started_at);
            updateServerChrome();
            if (data.setup_needed) {
                showSetup();
                return false;
            }
            if (!data.logged_in) {
                showLogin();
                return false;
            }
            state.connected = true;
            setConnection(true);
            return true;
        } catch (err) {
            setConnection(false);
            toast('Cannot reach the PyCloud server. Check that the process is running.', 'error');
            return false;
        }
    }

    async function submitLogin(event) {
        event.preventDefault();
        const input = $('#loginPassword');
        const feedback = $('#loginFeedback');
        const button = $('#btnSubmitLogin');
        const password = input ? input.value : '';
        if (feedback) feedback.textContent = '';
        if (!password) {
            if (feedback) feedback.textContent = 'Enter your password to continue.';
            if (input) input.focus();
            return;
        }

        if (button) button.disabled = true;
        try {
            const res = await apiFetch('/api/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ password: password })
            });
            let data = {};
            try { data = await res.json(); } catch (err) { /* empty body */ }
            if (!res.ok) {
                if (feedback) feedback.textContent = data.error || 'Authentication failed.';
                if (input) { input.value = ''; input.focus(); }
                return;
            }
            if (input) input.value = '';
            closeOverlay('loginModal');
            toast('Signed in. Welcome back.', 'success');
            initApp();
        } catch (err) {
            if (feedback) feedback.textContent = err.message || 'Login request failed.';
        } finally {
            if (button) button.disabled = false;
        }
    }

    async function submitSetup(event) {
        event.preventDefault();
        const passInput = $('#setupPassword');
        const confirmInput = $('#setupConfirmPassword');
        const feedback = $('#setupFeedback');
        const button = $('#btnSubmitSetup');
        const password = passInput ? passInput.value : '';
        const confirmation = confirmInput ? confirmInput.value : '';
        if (feedback) feedback.textContent = '';

        if (password.length < 8) {
            if (feedback) feedback.textContent = 'Use at least 8 characters.';
            if (passInput) passInput.focus();
            return;
        }
        if (password !== confirmation) {
            if (feedback) feedback.textContent = 'The two passwords do not match.';
            if (confirmInput) { confirmInput.value = ''; confirmInput.focus(); }
            return;
        }

        if (button) button.disabled = true;
        try {
            const res = await apiFetch('/api/setup', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ password: password, confirm_password: confirmation })
            });
            let data = {};
            try { data = await res.json(); } catch (err) { /* empty body */ }
            if (!res.ok) {
                if (feedback) feedback.textContent = data.error || 'Setup failed.';
                return;
            }
            if (passInput) passInput.value = '';
            if (confirmInput) confirmInput.value = '';
            closeOverlay('setupModal');
            toast('Setup complete. This server is now password protected.', 'success');
            initApp();
        } catch (err) {
            if (feedback) feedback.textContent = err.message || 'Setup request failed.';
        } finally {
            if (button) button.disabled = false;
        }
    }

    async function logout() {
        stopPolling();
        state.authenticated = false;
        try {
            await apiFetch('/api/logout', { method: 'POST' });
        } catch (err) {
            /* the session may already be gone; reload either way */
        }
        toast('Signed out.', 'info');
        setTimeout(() => window.location.reload(), 250);
    }

    function handleAuthFailure(err) {
        if (err && (err.code === 401 || err.code === 428)) {
            stopPolling();
            state.authenticated = false;
            return true;
        }
        return false;
    }

    /* ── Connection banner ─────────────────────────────────────────────── */

    function setConnection(ok) {
        state.connected = ok;
        const banner = $('#connBanner');
        if (banner) banner.hidden = ok;
        const dot = $('#serverDot');
        if (dot) dot.className = 'dot ' + (ok ? 'dot-ok' : 'dot-bad');
        setText('serverState', ok ? 'Server online' : 'Server unreachable');
    }

    function noteRequestSuccess() {
        state.connFails = 0;
        if (!state.connected) setConnection(true);
    }

    // Only genuine transport failures (a rejected fetch) or gateway errors should
    // reach here — never an ordinary HTTP error status. A 4xx/5xx reply proves
    // the server is reachable (e.g. a 404 when a library folder does not exist
    // yet), and counting it used to flash "server unreachable" mid-session.
    function noteRequestFailure(err) {
        if (err && err.name === 'AbortError') return;
        if (err && handleAuthFailure(err)) return;
        state.connFails += 1;
        if (state.connFails >= 2) setConnection(false);
    }

    /* ── Stats ─────────────────────────────────────────────────────────── */

    function renderSpark(container, values, color) {
        if (!container) return;
        clear(container);
        const width = 100;
        const height = 30;
        const pad = 2;
        if (values.length < 2) return;

        const min = Math.min.apply(null, values);
        const max = Math.max.apply(null, values);
        const range = max - min || 1;
        const points = values.map((value, index) => {
            const x = (index / (values.length - 1)) * width;
            const y = height - pad - ((value - min) / range) * (height - pad * 2);
            return x.toFixed(2) + ',' + y.toFixed(2);
        }).join(' ');

        const svg = document.createElementNS(SVG_NS, 'svg');
        svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
        svg.setAttribute('preserveAspectRatio', 'none');
        svg.setAttribute('aria-hidden', 'true');

        const area = document.createElementNS(SVG_NS, 'polygon');
        area.setAttribute('points', '0,' + height + ' ' + points + ' ' + width + ',' + height);
        area.style.fill = color;
        area.style.opacity = '0.08';

        const line = document.createElementNS(SVG_NS, 'polyline');
        line.setAttribute('points', points);
        line.style.fill = 'none';
        line.style.stroke = color;
        line.style.strokeWidth = '1.4';
        line.style.vectorEffect = 'non-scaling-stroke';
        line.style.strokeLinejoin = 'round';
        line.style.strokeLinecap = 'round';

        svg.appendChild(area);
        svg.appendChild(line);
        container.appendChild(svg);
    }

    function renderStats(data) {
        setServerUptime(data.uptime_seconds, data.started_at);
        setText('statCpu', data.cpu_load || '—');
        setText('statRam', data.ram_usage || '—');

        const cpu = parseMetric(data.cpu_load);
        const ram = parseMetric(data.ram_usage);
        if (cpu !== null) {
            state.statsHistory.cpu.push(cpu);
            if (state.statsHistory.cpu.length > 40) state.statsHistory.cpu.shift();
        }
        if (ram !== null) {
            state.statsHistory.ram.push(ram);
            if (state.statsHistory.ram.length > 40) state.statsHistory.ram.shift();
        }
        renderSpark($('#sparkCpu'), state.statsHistory.cpu, 'var(--accent)');
        renderSpark($('#sparkRam'), state.statsHistory.ram, 'var(--video)');

        const pct = parseMetric(data.disk_pct);
        const safePct = pct === null ? 0 : Math.max(0, Math.min(100, pct));
        const ring = $('#diskRing');
        if (ring) {
            ring.style.setProperty('--pct', String(safePct));
            ring.setAttribute('aria-label', pct === null ? 'Disk usage unavailable' : 'Disk ' + safePct.toFixed(1) + ' percent used');
        }
        setText('diskPct', pct === null ? '—' : Math.round(safePct) + '%');
        setText('diskFree', data.free_gb || '—');
        setText('diskTotal', 'of ' + (data.total_gb || '—'));

        setText('systemState', state.connected ? 'All systems nominal' : 'Telemetry unavailable');
        renderStorage();
    }

    async function updateStats() {
        if (!state.authenticated) return;
        try {
            const res = await apiFetch('/api/stats');
            if (!res.ok) throw new Error('Stats request failed');
            const data = await res.json();
            state.stats = data;
            noteRequestSuccess();
            renderStats(data);
        } catch (err) {
            /* connectivity is reported centrally by apiFetch */
        }
    }
    /* ── Library helpers ───────────────────────────────────────────────── */

    const LIBRARY_PATHS = {
        uploads: 'media/uploads',
        youtube: 'media/yt_library',
        trash: '.pycloud_trash'
    };

    const LIBRARY_LABELS = {
        uploads: 'Uploads',
        youtube: 'YouTube',
        trash: 'Trash'
    };

    function categoryForPath(path) {
        if (!path) return 'all';
        if (path === LIBRARY_PATHS.uploads || path.indexOf(LIBRARY_PATHS.uploads + '/') === 0) return 'uploads';
        if (path === LIBRARY_PATHS.youtube || path.indexOf(LIBRARY_PATHS.youtube + '/') === 0) return 'youtube';
        if (path === LIBRARY_PATHS.trash || path.indexOf(LIBRARY_PATHS.trash + '/') === 0) return 'trash';
        return 'all';
    }

    function isTrashPath(path) {
        return path === LIBRARY_PATHS.trash || path.indexOf(LIBRARY_PATHS.trash + '/') === 0;
    }

    function joinPath(parent, name) {
        return parent ? parent + '/' + name : name;
    }

    function parentOf(path) {
        const index = path.lastIndexOf('/');
        return index < 0 ? '' : path.slice(0, index);
    }

    function fileKind(file) {
        if (!file) return 'other';
        if (file.is_dir) return 'folder';
        return file.type || 'other';
    }

    function kindIcon(kind) {
        if (kind === 'folder') return 'folder';
        if (kind === 'video') return 'film';
        if (kind === 'audio') return 'music';
        if (kind === 'image') return 'image';
        if (kind === 'archive') return 'archive';
        return 'file';
    }

    function glyph(file) {
        const kind = fileKind(file);
        const wrap = el('span', 'file-glyph ft-' + kind);
        wrap.appendChild(icon(kindIcon(kind)));
        return wrap;
    }

    function streamUrl(path) {
        return '/api/media/stream?path=' + encodeURIComponent(path);
    }

    function downloadUrl(path) {
        return '/api/download?path=' + encodeURIComponent(path) + '&dl=1';
    }

    function isMediaFile(file) {
        return Boolean(file) && !file.is_dir && (file.type === 'video' || file.type === 'audio' || file.type === 'image');
    }

    function isPlayable(file) {
        if (!isMediaFile(file)) return false;
        const ext = (file.ext || '').toLowerCase();
        if (file.type === 'image') return true;
        if (file.type === 'audio') return ['.mp3', '.wav', '.flac', '.aac', '.ogg', '.m4a', '.opus'].indexOf(ext) >= 0;
        return ['.mp4', '.webm', '.mov', '.m4v', '.ogv'].indexOf(ext) >= 0;
    }

    /* ── Library counts / storage ──────────────────────────────────────── */

    async function loadLibraries(force) {
        if (!state.authenticated) return;
        if (!force && Date.now() - state.libraryAt < 20000) return;
        state.libraryAt = Date.now();

        const keys = Object.keys(LIBRARY_PATHS);
        const results = await Promise.allSettled(keys.map((key) => fetchDir(LIBRARY_PATHS[key])));

        results.forEach((result, index) => {
            const key = keys[index];
            if (result.status !== 'fulfilled') return;
            const files = result.value.files || [];
            let size = 0;
            files.forEach((file) => {
                if (!file.is_dir) size += Number(file.size) || 0;
            });
            state.libraryCounts[key] = files.length;
            state.librarySizes[key] = size;
        });

        renderLibraryCounts();
        renderStorage();
    }

    function renderLibraryCounts() {
        Object.keys(LIBRARY_LABELS).forEach((key) => {
            const count = state.libraryCounts[key];
            const text = count === null ? '—' : String(count);
            $$('[data-count="' + key + '"]').forEach((node) => { node.textContent = text; });
            $$('[data-chip-count="' + key + '"]').forEach((node) => { node.textContent = text; });
        });
        const allChips = $$('[data-chip-count="all"]');
        const total = Object.keys(LIBRARY_LABELS).reduce((sum, key) => {
            const count = state.libraryCounts[key];
            return sum + (count === null ? 0 : count);
        }, 0);
        allChips.forEach((node) => { node.textContent = String(total); });
    }

    function renderStorage() {
        const uploads = state.librarySizes.uploads || 0;
        const youtube = state.librarySizes.youtube || 0;
        const trash = state.librarySizes.trash || 0;
        const other = state.rootOtherSize || 0;
        const total = uploads + youtube + trash + other;

        const bar = $('#storageBar');
        if (bar) {
            const segments = [
                ['.seg-uploads', uploads],
                ['.seg-youtube', youtube],
                ['.seg-trash', trash],
                ['.seg-other', other]
            ];
            segments.forEach(([selector, value]) => {
                const node = bar.querySelector(selector);
                if (!node) return;
                node.style.width = total > 0 ? (value / total) * 100 + '%' : '0%';
            });
        }

        setText('storageSub', total > 0 ? formatBytes(total) + ' tracked' : 'No tracked files');
        $$('[data-storage="uploads"]').forEach((node) => { node.textContent = formatBytes(uploads); });
        $$('[data-storage="youtube"]').forEach((node) => { node.textContent = formatBytes(youtube); });
        $$('[data-storage="trash"]').forEach((node) => { node.textContent = formatBytes(trash); });
        $$('[data-storage="other"]').forEach((node) => { node.textContent = formatBytes(other); });
    }

    /* ── Files: load + render ──────────────────────────────────────────── */

    function setFilesLoading() {
        const list = $('#filesList');
        if (!list) return;
        clear(list);
        for (let i = 0; i < 4; i += 1) {
            const row = el('div', 'skeleton-row');
            row.appendChild(el('span', 'skeleton'));
            row.appendChild(el('span', 'skeleton'));
            row.appendChild(el('span', 'skeleton'));
            list.appendChild(row);
        }
    }

    async function loadFiles(path, options) {
        const opts = options || {};
        if (!state.authenticated) return;
        const target = path || '';
        if (!opts.silent) setFilesLoading();
        const token = ++state.filesToken;

        try {
            const data = await fetchDir(target);
            if (token !== state.filesToken) return;

            state.path = data.current_path || '';
            state.files = Array.isArray(data.files) ? data.files : [];
            state.filesLoaded = true;
            state.filesAt = Date.now();
            state.category = categoryForPath(state.path);

            if (state.path === '' && !state.rootOtherSizeAt) {
                let rootFiles = 0;
                state.files.forEach((file) => {
                    if (!file.is_dir) rootFiles += Number(file.size) || 0;
                });
                state.rootOtherSize = rootFiles;
                state.rootOtherSizeAt = Date.now();
            }

            renderBreadcrumbs();
            renderCategoryChips();
            applyFileFilter();
            updateUploadTarget();
            updateTrashActions();
            renderStorage();
            noteRequestSuccess();
        } catch (err) {
            if (token !== state.filesToken) return;
            if (handleAuthFailure(err)) return;
            const list = $('#filesList');
            if (list) {
                clear(list);
                const message = el('p', 'empty-inline', err.message || 'Could not open this folder.');
                list.appendChild(message);
            }
            toast(err.message || 'Could not open this folder.', 'error');
        }
    }

    function renderBreadcrumbs() {
        const nav = $('#breadcrumbs');
        if (!nav) return;
        clear(nav);

        const segments = state.path ? state.path.split('/').filter(Boolean) : [];
        const root = el('button', 'crumb' + (segments.length === 0 ? ' is-current' : ''), 'root');
        root.type = 'button';
        root.addEventListener('click', () => { if (segments.length) loadFiles(''); });
        nav.appendChild(root);

        let accumulated = '';
        segments.forEach((segment, index) => {
            accumulated = joinPath(accumulated, segment);
            const sep = el('span', 'crumb-sep', '/');
            sep.setAttribute('aria-hidden', 'true');
            nav.appendChild(sep);

            const label = segment === '.pycloud_trash' ? 'trash' : segment;
            const last = index === segments.length - 1;
            const button = el('button', 'crumb' + (last ? ' is-current' : ''), label);
            button.type = 'button';
            if (!last) {
                const target = accumulated;
                button.addEventListener('click', () => loadFiles(target));
            }
            nav.appendChild(button);
        });
    }

    function renderCategoryChips() {
        $$('#categoryChips .chip').forEach((chip) => {
            const active = chip.dataset.cat === state.category;
            chip.classList.toggle('is-active', active);
            chip.setAttribute('aria-pressed', active ? 'true' : 'false');
        });
        const trashNotice = $('#trashNotice');
        if (trashNotice) trashNotice.hidden = state.category !== 'trash';
    }

    function sortFiles(files) {
        const sorted = files.slice();
        if (state.sort === 'name') {
            sorted.sort((a, b) => {
                if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
                return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
            });
        } else if (state.sort === 'size') {
            sorted.sort((a, b) => {
                if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
                return (Number(b.size) || 0) - (Number(a.size) || 0);
            });
        } else {
            sorted.sort((a, b) => {
                if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
                return (Number(b.mod_time) || 0) - (Number(a.mod_time) || 0);
            });
        }
        return sorted;
    }

    function applyFileFilter() {
        const query = ($('#searchInput') && $('#searchInput').value ? $('#searchInput').value : '').trim().toLowerCase();
        const clearButton = $('#btnClearSearch');
        if (clearButton) clearButton.hidden = query.length === 0;

        state.filteredFiles = sortFiles(state.files.filter((file) => {
            if (!query) return true;
            return file.name.toLowerCase().indexOf(query) >= 0;
        }));

        renderFileRows();
    }

    function renderFileRows() {
        const list = $('#filesList');
        if (!list) return;
        clear(list);

        if (state.filteredFiles.length === 0) {
            const message = el('p', 'empty-inline');
            if (state.files.length === 0) {
                message.textContent = state.category === 'trash' ? 'Trash is empty.' : 'This folder is empty.';
            } else {
                message.textContent = 'No files match the current filter.';
            }
            list.appendChild(message);
        } else {
            state.filteredFiles.forEach((file) => {
                list.appendChild(buildFileRow(file));
            });
        }

        const count = state.filteredFiles.length;
        setText('fileCountBadge', count + (count === 1 ? ' item' : ' items'));
        let total = 0;
        state.filteredFiles.forEach((file) => {
            if (!file.is_dir) total += Number(file.size) || 0;
        });
        setText('fileTotalSize', formatBytes(total));
    }

    function buildFileRow(file) {
        const row = el('div', 'file-row');
        row.dataset.path = file.path;

        const nameCell = el('div', 'col-name');
        nameCell.appendChild(glyph(file));
        const nameButton = el('button', 'file-name', file.name);
        nameButton.type = 'button';
        nameButton.title = file.name;
        nameButton.addEventListener('click', () => openFile(file));
        nameCell.appendChild(nameButton);
        row.appendChild(nameCell);

        row.appendChild(el('span', 'col-type', file.is_dir ? 'folder' : (file.ext || 'file').replace('.', '')));

        const size = el('span', 'col-size', file.is_dir ? '—' : (file.human_size || formatBytes(file.size)));
        row.appendChild(size);

        row.appendChild(el('span', 'col-date', formatDate(file.mod_time)));

        const actions = el('div', 'col-actions');

        if (isPlayable(file)) {
            const play = rowButton('play', 'Play ' + file.name, () => openPlayer(file));
            actions.appendChild(play);
        }

        if (!file.is_dir) {
            const download = rowButton('download', 'Download ' + file.name, () => {
                const anchor = document.createElement('a');
                anchor.href = downloadUrl(file.path);
                anchor.download = file.name;
                document.body.appendChild(anchor);
                anchor.click();
                anchor.remove();
            });
            actions.appendChild(download);
        }

        actions.appendChild(rowButton('pencil', 'Rename ' + file.name, () => openRename(file)));
        actions.appendChild(rowButton('trash', 'Delete ' + file.name, () => deleteFile(file), true));

        row.appendChild(actions);
        return row;
    }

    function rowButton(iconName, label, handler, danger) {
        const button = el('button', 'row-btn' + (danger ? ' is-danger' : ''));
        button.type = 'button';
        button.title = label;
        button.setAttribute('aria-label', label);
        button.appendChild(icon(iconName));
        button.addEventListener('click', (event) => {
            event.stopPropagation();
            handler();
        });
        return button;
    }

    function openFile(file) {
        if (!file) return;
        if (file.is_dir) {
            state.filesLoaded = false;
            loadFiles(file.path);
            return;
        }
        if (isMediaFile(file)) {
            openPlayer(file);
        } else {
            const anchor = document.createElement('a');
            anchor.href = downloadUrl(file.path);
            anchor.download = file.name;
            document.body.appendChild(anchor);
            anchor.click();
            anchor.remove();
        }
    }

    /* ── Files: rename / mkdir / delete ────────────────────────────────── */

    function openRename(file) {
        if (!file) return;
        setText('renameTitle', file.is_dir ? 'Rename folder' : 'Rename file');
        const input = $('#renameInput');
        const oldPath = $('#renameTargetOldPath');
        const feedback = $('#renameFeedback');
        if (feedback) feedback.textContent = '';
        if (input) input.value = file.name;
        if (oldPath) oldPath.value = file.path;
        openOverlay('renameModal', { focus: '#renameInput' });
        if (input) {
            setTimeout(() => {
                const dot = file.name.lastIndexOf('.');
                if (!file.is_dir && dot > 0) input.setSelectionRange(0, dot);
                else input.select();
            }, 40);
        }
    }

    async function submitRename(event) {
        event.preventDefault();
        const input = $('#renameInput');
        const oldPath = $('#renameTargetOldPath');
        const feedback = $('#renameFeedback');
        const button = $('#btnSubmitRename');
        if (!input || !oldPath) return;
        const newName = input.value.trim();
        if (!newName) {
            if (feedback) feedback.textContent = 'Enter a new name.';
            input.focus();
            return;
        }
        if (newName.indexOf('/') >= 0 || newName.indexOf('\\') >= 0) {
            if (feedback) feedback.textContent = 'Names cannot contain slashes.';
            input.focus();
            return;
        }
        if (feedback) feedback.textContent = '';
        if (button) button.disabled = true;
        try {
            const res = await apiFetch('/api/rename', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ old_path: oldPath.value, new_name: newName })
            });
            if (!res.ok) {
                if (feedback) feedback.textContent = await readError(res, 'Rename failed.');
                return;
            }
            closeOverlay('renameModal');
            toast('Renamed to “' + newName + '”.', 'success');
            await loadFiles(state.path, { silent: true });
            loadLibraries(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            if (feedback) feedback.textContent = err.message || 'Rename failed.';
        } finally {
            if (button) button.disabled = false;
        }
    }

    function openMkdir() {
        if (isTrashPath(state.path)) {
            toast('Trash is read-only. Open another folder first.', 'error');
            return;
        }
        const feedback = $('#mkdirFeedback');
        const input = $('#mkdirInput');
        if (feedback) feedback.textContent = '';
        if (input) input.value = '';
        setText('mkdirLocation', state.path ? 'Inside /' + state.path : 'Inside root');
        openOverlay('mkdirModal', { focus: '#mkdirInput' });
    }

    async function submitMkdir(event) {
        event.preventDefault();
        const input = $('#mkdirInput');
        const feedback = $('#mkdirFeedback');
        const button = $('#btnSubmitMkdir');
        if (!input) return;
        const name = input.value.trim();
        if (!name) {
            if (feedback) feedback.textContent = 'Enter a folder name.';
            input.focus();
            return;
        }
        if (name.indexOf('/') >= 0 || name.indexOf('\\') >= 0) {
            if (feedback) feedback.textContent = 'Names cannot contain slashes.';
            input.focus();
            return;
        }
        if (feedback) feedback.textContent = '';
        if (button) button.disabled = true;
        try {
            const res = await apiFetch('/api/mkdir', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ parent_path: state.path || '', dir_name: name })
            });
            if (!res.ok) {
                if (feedback) feedback.textContent = await readError(res, 'Could not create the folder.');
                return;
            }
            closeOverlay('mkdirModal');
            toast('Created folder “' + name + '”.', 'success');
            await loadFiles(state.path, { silent: true });
            loadLibraries(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            if (feedback) feedback.textContent = err.message || 'Could not create the folder.';
        } finally {
            if (button) button.disabled = false;
        }
    }

    async function deleteFile(file) {
        if (!file) return;
        const inTrash = isTrashPath(file.path);
        const confirmed = await confirmAction({
            title: inTrash ? 'Delete permanently?' : 'Move to trash?',
            message: inTrash
                ? '“' + file.name + '” will be erased from disk. This cannot be undone.'
                : '“' + file.name + '” will be moved to .pycloud_trash. Deleting it again from Trash erases it permanently.',
            confirmLabel: inTrash ? 'Delete forever' : 'Move to trash',
            requireText: inTrash ? 'delete' : '',
            danger: true
        });
        if (!confirmed) return;

        try {
            const res = await apiFetch('/api/delete', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: file.path, permanent: inTrash })
            });
            if (!res.ok) {
                toast(await readError(res, 'Delete failed.'), 'error');
                return;
            }
            toast(inTrash ? 'Deleted “' + file.name + '” permanently.' : 'Moved “' + file.name + '” to Trash.', 'success', inTrash ? null : {
                label: 'View trash',
                onClick: () => openLibrary('trash')
            });
            await loadFiles(state.path, { silent: true });
            loadLibraries(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            toast(err.message || 'Delete failed.', 'error');
        }
    }

    /* ── Uploads ───────────────────────────────────────────────────────── */

    function uploadTargetPath() {
        if (state.path && !isTrashPath(state.path)) return state.path;
        return LIBRARY_PATHS.uploads;
    }

    function updateUploadTarget() {
        const target = uploadTargetPath();
        setText('uploadTarget', target);
        const zone = $('#dropZone');
        if (zone) zone.classList.toggle('is-disabled', isTrashPath(state.path));
    }

    function updateTrashActions() {
        const btn = $('#btnEmptyTrash');
        if (!btn) return;
        btn.hidden = !isTrashPath(state.path);
    }

    async function clearTrash() {
        if (!isTrashPath(state.path)) return;
        const total = Array.isArray(state.files) ? state.files.length : 0;
        const confirmed = await confirmAction({
            title: 'Clear all Trash?',
            message: total > 0
                ? 'All ' + total + ' item' + (total === 1 ? '' : 's') + ' in Trash will be erased from disk. This cannot be undone.'
                : 'Trash is already empty.',
            confirmLabel: 'Clear all',
            requireText: 'clear',
            danger: true
        });
        if (!confirmed) return;

        const btn = $('#btnEmptyTrash');
        if (btn) btn.disabled = true;
        try {
            const res = await apiFetch('/api/trash/empty', { method: 'POST' });
            if (!res.ok) {
                toast(await readError(res, 'Could not clear Trash.'), 'error');
                return;
            }
            const data = await res.json().catch(() => null);
            const deleted = data && Number.isFinite(Number(data.deleted)) ? Number(data.deleted) : 0;
            toast(deleted ? 'Trash cleared · ' + deleted + ' item' + (deleted === 1 ? '' : 's') + ' removed.' : 'Trash is already empty.', deleted ? 'success' : 'info');
            state.path = LIBRARY_PATHS.trash;
            state.filesLoaded = false;
            await loadFiles(LIBRARY_PATHS.trash, { silent: true });
            loadLibraries(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            toast(err.message || 'Could not clear Trash.', 'error');
        } finally {
            if (btn) btn.disabled = false;
            updateTrashActions();
        }
    }

    function openUploadPicker() {
        if (isTrashPath(state.path)) {
            toast('Trash is read-only for uploads. Open another folder first.', 'error');
            return;
        }
        if (state.view !== 'files') go('files');
        const input = $('#fileInput');
        if (input) input.click();
    }

    function handleFileInput(event) {
        const input = event.target;
        if (!input || !input.files || input.files.length === 0) return;
        const files = Array.from(input.files);
        input.value = '';
        uploadFiles(files);
    }

    function handleDrop(event) {
        event.preventDefault();
        const zone = $('#dropZone');
        if (zone) zone.classList.remove('is-dragover');
        if (isTrashPath(state.path)) {
            toast('Trash is read-only for uploads. Open another folder first.', 'error');
            return;
        }
        const files = event.dataTransfer && event.dataTransfer.files ? Array.from(event.dataTransfer.files) : [];
        if (files.length) uploadFiles(files);
    }

    function setUploadPanel(visible) {
        const panel = $('#uploadProgressContainer');
        if (panel) panel.hidden = !visible;
    }

    function uploadFiles(files) {
        if (!files.length) return;
        if (state.activeUploadXHR) {
            toast('An upload is already running. Wait for it to finish or cancel it.', 'error');
            return;
        }
        const target = uploadTargetPath();
        uploadSequentially(files, target, 0, 0);
    }

    async function uploadSequentially(files, target, index, succeeded) {
        if (index >= files.length) {
            setUploadPanel(false);
            toast(succeeded + (succeeded === 1 ? ' file uploaded to ' : ' files uploaded to ') + target + '.', 'success');
            await loadFiles(state.path, { silent: true });
            loadLibraries(true);
            return;
        }

        const file = files[index];
        setUploadPanel(true);
        setText('uploadFilename', file.name);
        setText('uploadPercentage', '0%');
        const fill = $('#uploadProgressBar');
        if (fill) fill.style.width = '0%';

        try {
            await uploadOne(file, target, (percent) => {
                setText('uploadPercentage', percent + '%');
                if (fill) fill.style.width = percent + '%';
            });
            uploadSequentially(files, target, index + 1, succeeded + 1);
        } catch (err) {
            setUploadPanel(false);
            if (err && err.name === 'AbortError') {
                toast('Upload cancelled.', 'info');
            } else {
                toast(err && err.message ? err.message : 'Upload failed.', 'error');
            }
            await loadFiles(state.path, { silent: true });
        }
    }

    function uploadOne(file, target, onProgress) {
        return new Promise((resolve, reject) => {
            const xhr = new XMLHttpRequest();
            state.activeUploadXHR = xhr;
            xhr.open('POST', '/api/upload');
            const csrf = getCSRFToken();
            if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf);

            xhr.upload.addEventListener('progress', (event) => {
                if (!event.lengthComputable) return;
                const percent = Math.min(100, Math.round((event.loaded / event.total) * 100));
                onProgress(percent);
            });

            xhr.addEventListener('load', () => {
                state.activeUploadXHR = null;
                if (xhr.status >= 200 && xhr.status < 300) {
                    onProgress(100);
                    resolve();
                    return;
                }
                let message = 'Upload failed.';
                try {
                    const data = JSON.parse(xhr.responseText);
                    if (data && data.error) message = data.error;
                } catch (err) {
                    if (xhr.status === 413) message = 'The file exceeds the server upload limit.';
                }
                reject(new Error(message));
            });

            xhr.addEventListener('error', () => {
                state.activeUploadXHR = null;
                reject(new Error('Network error while uploading.'));
            });

            xhr.addEventListener('abort', () => {
                state.activeUploadXHR = null;
                const error = new Error('Upload cancelled');
                error.name = 'AbortError';
                reject(error);
            });

            const form = new FormData();
            form.append('path', target);
            form.append('file', file, file.name);
            xhr.send(form);
        });
    }

    function cancelUpload() {
        if (state.activeUploadXHR) {
            state.activeUploadXHR.abort();
        }
    }

    /* ── Recent files ──────────────────────────────────────────────────── */

    function renderRecent(files) {
        const container = $('#recentFiles');
        if (!container) return;
        clear(container);

        const recent = files
            .filter((file) => !file.is_dir)
            .sort((a, b) => (Number(b.mod_time) || 0) - (Number(a.mod_time) || 0))
            .slice(0, 6);

        if (recent.length === 0) {
            container.appendChild(el('p', 'empty-inline', 'No files yet. Upload something or fetch media to get started.'));
            return;
        }

        recent.forEach((file) => {
            const row = el('div', 'mini-row');
            row.appendChild(glyph(file));
            const name = el('button', 'mini-name', file.name);
            name.type = 'button';
            name.title = file.path;
            name.addEventListener('click', () => openFile(file));
            row.appendChild(name);
            row.appendChild(el('span', 'mini-size', file.human_size || formatBytes(file.size)));
            row.appendChild(el('span', 'mini-date', formatDate(file.mod_time)));
            container.appendChild(row);
        });
    }
    /* ── Media library ─────────────────────────────────────────────────── */

    function mediaCounts() {
        const counts = { all: 0, video: 0, audio: 0, image: 0 };
        state.mediaFiles.forEach((file) => {
            counts.all += 1;
            if (counts[file.type] !== undefined) counts[file.type] += 1;
        });
        return counts;
    }

    function mediaItemsForFilter() {
        if (state.mediaFilter === 'all') return state.mediaFiles;
        return state.mediaFiles.filter((file) => file.type === state.mediaFilter);
    }

    async function loadMedia(force) {
        if (!state.authenticated) return;
        if (state.mediaLoading) return;
        if (!force && state.mediaLoaded && Date.now() - state.mediaAt < 15000) return;

        state.mediaLoading = true;
        const summary = $('#mediaSummary');
        if (summary && !state.mediaLoaded) summary.textContent = 'Scanning libraries…';

        try {
            const keys = ['uploads', 'youtube'];
            const results = await Promise.allSettled(keys.map((key) => fetchDir(LIBRARY_PATHS[key])));
            const collected = [];

            results.forEach((result, index) => {
                if (result.status !== 'fulfilled') return;
                const files = result.value.files || [];
                files.forEach((file) => {
                    if (file.is_dir) return;
                    if (file.type !== 'video' && file.type !== 'audio' && file.type !== 'image') return;
                    collected.push(file);
                });
                const key = keys[index];
                state.libraryCounts[key] = files.length;
                let size = 0;
                files.forEach((file) => {
                    if (!file.is_dir) size += Number(file.size) || 0;
                });
                state.librarySizes[key] = size;
            });

            collected.sort((a, b) => (Number(b.mod_time) || 0) - (Number(a.mod_time) || 0));
            state.mediaFiles = collected;
            state.mediaLoaded = true;
            state.mediaAt = Date.now();

            renderMedia();
            renderLibraryCounts();
            renderStorage();
            noteRequestSuccess();
        } catch (err) {
            if (handleAuthFailure(err)) return;
            if (summary) summary.textContent = 'Could not scan the media libraries.';
        } finally {
            state.mediaLoading = false;
        }
    }

    function renderMedia() {
        const counts = mediaCounts();
        $$('[data-media-count="all"]').forEach((node) => { node.textContent = String(counts.all); });
        $$('[data-media-count="video"]').forEach((node) => { node.textContent = String(counts.video); });
        $$('[data-media-count="audio"]').forEach((node) => { node.textContent = String(counts.audio); });
        $$('[data-media-count="image"]').forEach((node) => { node.textContent = String(counts.image); });

        $$('#mediaChips .chip').forEach((chip) => {
            const active = chip.dataset.mediaFilter === state.mediaFilter;
            chip.classList.toggle('is-active', active);
            chip.setAttribute('aria-pressed', active ? 'true' : 'false');
        });

        let totalSize = 0;
        state.mediaFiles.forEach((file) => { totalSize += Number(file.size) || 0; });
        setText('mediaSummary', counts.all === 0
            ? 'No playable media found'
            : counts.video + ' video · ' + counts.audio + ' audio · ' + counts.image + ' image · ' + formatBytes(totalSize));

        const items = mediaItemsForFilter();
        const visual = items.filter((file) => file.type === 'video' || file.type === 'image');
        const audio = items.filter((file) => file.type === 'audio');

        const grid = $('#mediaGrid');
        if (grid) {
            releaseMediaVideos();
            clear(grid);
            visual.forEach((file) => {
                grid.appendChild(buildMediaCard(file));
            });
        }

        const audioSection = $('#audioSection');
        const audioList = $('#audioList');
        if (audioSection && audioList) {
            clear(audioList);
            if (audio.length > 0) {
                audioSection.hidden = false;
                setText('audioSub', audio.length + (audio.length === 1 ? ' track' : ' tracks'));
                audio.forEach((file) => {
                    audioList.appendChild(buildAudioRow(file));
                });
            } else {
                audioSection.hidden = true;
            }
        }

        const empty = $('#mediaEmpty');
        if (empty) {
            const nothing = state.mediaLoaded && items.length === 0;
            empty.hidden = !nothing;
            if (nothing) {
                const title = empty.querySelector('strong');
                const copy = empty.querySelector('p');
                if (title && copy) {
                    if (state.mediaFiles.length === 0) {
                        title.textContent = 'No media in the libraries yet';
                        copy.textContent = 'Upload a video, image or audio file — or fetch one from YouTube.';
                    } else {
                        title.textContent = 'Nothing in this filter';
                        copy.textContent = 'Choose another media type to see the rest of the library.';
                    }
                }
            }
        }

        observeThumbnails();
    }

    function buildMediaCard(file) {
        const card = el('button', 'media-card');
        card.type = 'button';
        card.title = file.name + ' — ' + (file.human_size || formatBytes(file.size));

        const thumb = el('div', 'media-thumb');
        if (file.type === 'video') {
            // Only formats a browser can usually decode are probed for a poster
            // frame. Others get a static glyph so we never open a dead stream.
            if (isPlayable(file)) {
                const video = document.createElement('video');
                video.muted = true;
                video.playsInline = true;
                video.preload = 'metadata';
                video.dataset.thumbSrc = streamUrl(file.path);
                thumb.appendChild(video);
            } else {
                const fallback = el('span', 'media-fallback');
                fallback.appendChild(icon('film'));
                thumb.appendChild(fallback);
            }
        } else {
            const image = document.createElement('img');
            image.loading = 'lazy';
            image.decoding = 'async';
            image.alt = '';
            image.src = streamUrl(file.path);
            image.addEventListener('error', () => {
                image.remove();
                const fallback = el('span', 'media-fallback');
                fallback.appendChild(icon('image'));
                thumb.appendChild(fallback);
            });
            thumb.appendChild(image);
        }

        const play = el('span', 'media-play');
        play.appendChild(icon(file.type === 'video' ? 'play' : 'image'));
        thumb.appendChild(play);
        card.appendChild(thumb);

        const body = el('div', 'media-body');
        body.appendChild(el('span', 'media-name', file.name));
        const meta = el('div', 'media-meta');
        meta.appendChild(el('span', null, file.type.toUpperCase()));
        meta.appendChild(el('span', null, file.human_size || formatBytes(file.size)));
        body.appendChild(meta);
        card.appendChild(body);

        card.addEventListener('click', () => openPlayer(file));
        return card;
    }

    function buildAudioRow(file) {
        const row = el('button', 'audio-row');
        row.type = 'button';
        const ico = el('span', 'file-glyph ft-audio');
        ico.appendChild(icon('music'));
        row.appendChild(ico);

        const name = el('span', 'audio-name');
        name.appendChild(el('strong', null, file.name));
        name.appendChild(el('span', null, formatDate(file.mod_time)));
        row.appendChild(name);

        row.appendChild(el('span', 'audio-size', file.human_size || formatBytes(file.size)));

        const play = el('span', 'audio-play');
        play.appendChild(icon('play'));
        row.appendChild(play);

        row.addEventListener('click', () => openPlayer(file));
        return row;
    }

    function observeThumbnails() {
        const grid = $('#mediaGrid');
        if (!grid) return;
        const videos = $$('video[data-thumb-src]', grid);

        if (state.thumbObserver) {
            state.thumbObserver.disconnect();
            state.thumbObserver = null;
        }

        if (!('IntersectionObserver' in window)) {
            videos.forEach((video) => probeThumbnail(video));
            return;
        }

        state.thumbObserver = new IntersectionObserver((entries) => {
            entries.forEach((entry) => {
                if (!entry.isIntersecting) return;
                const video = entry.target;
                state.thumbObserver.unobserve(video);
                probeThumbnail(video);
            });
        }, { rootMargin: '180px' });

        videos.forEach((video) => state.thumbObserver.observe(video));
    }

    /**
     * Load just enough of a video to draw one frame into a canvas, then release
     * the element. Releasing matters: a video element left attached keeps its
     * HTTP connection open, and a page full of them can exhaust the browser's
     * per-host connection pool.
     */
    function probeThumbnail(video) {
        const thumb = video.parentNode;
        if (!thumb) return;
        let settled = false;
        let timer = null;

        const release = () => {
            try {
                video.pause();
                video.removeAttribute('src');
                video.load();
            } catch (err) { /* element may already be detached */ }
        };

        const fallback = () => {
            release();
            if (video.parentNode) {
                const node = el('span', 'media-fallback');
                node.appendChild(icon('film'));
                video.parentNode.replaceChild(node, video);
            }
        };

        const capture = () => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            let drawn = false;
            try {
                if (video.videoWidth > 0 && video.videoHeight > 0) {
                    const canvas = document.createElement('canvas');
                    const width = Math.min(360, video.videoWidth);
                    canvas.width = width;
                    canvas.height = Math.round(width * (video.videoHeight / video.videoWidth));
                    canvas.className = 'media-thumb-frame';
                    canvas.setAttribute('aria-hidden', 'true');
                    canvas.getContext('2d').drawImage(video, 0, 0, canvas.width, canvas.height);
                    drawn = true;
                    release();
                    if (video.parentNode) video.parentNode.replaceChild(canvas, video);
                }
            } catch (err) {
                drawn = false;
            }
            if (!drawn) fallback();
        };

        video.addEventListener('loadedmetadata', () => {
            const target = Number.isFinite(video.duration) && video.duration > 1.2 ? 0.6 : 0;
            try { video.currentTime = target; } catch (err) { /* ignore */ }
        });
        video.addEventListener('seeked', () => setTimeout(capture, 60), { once: true });
        video.addEventListener('loadeddata', () => setTimeout(capture, 220));
        video.addEventListener('error', fallback, { once: true });
        timer = setTimeout(fallback, 5000);

        video.src = video.dataset.thumbSrc;
    }

    function releaseMediaVideos() {
        if (state.thumbObserver) {
            state.thumbObserver.disconnect();
            state.thumbObserver = null;
        }
        $$('#mediaGrid video').forEach((video) => {
            try {
                video.pause();
                video.removeAttribute('src');
                video.load();
            } catch (err) { /* ignore */ }
        });
    }

    /* ── Player ────────────────────────────────────────────────────────── */

    let playerAbort = null;

    function openPlayer(file) {
        if (!file) return;
        const stage = $('#cinemaContent');
        if (!stage) return;
        clear(stage);

        const kind = file.type === 'audio' ? 'audio' : file.type === 'image' ? 'image' : 'video';
        setText('playerKind', kind.toUpperCase());
        setText('cinemaTitle', file.name);
        setText('playerMeta', (file.human_size || formatBytes(file.size)) + ' · ' + (file.ext || '').replace('.', '').toUpperCase());

        const download = $('#cinemaDlBtn');
        if (download) {
            download.href = downloadUrl(file.path);
            download.setAttribute('download', file.name);
        }

        if (kind === 'audio') {
            const audio = document.createElement('audio');
            audio.controls = true;
            audio.autoplay = true;
            audio.preload = 'metadata';
            audio.src = streamUrl(file.path);
            audio.addEventListener('error', () => showPlayerError(stage, 'This audio format cannot be played in the browser. Download it instead.'));
            stage.appendChild(audio);
        } else if (kind === 'image') {
            const image = document.createElement('img');
            image.alt = file.name;
            image.src = streamUrl(file.path);
            image.addEventListener('error', () => showPlayerError(stage, 'The image could not be loaded.'));
            stage.appendChild(image);
        } else {
            const video = document.createElement('video');
            video.controls = true;
            video.autoplay = true;
            video.playsInline = true;
            video.preload = 'metadata';
            video.src = streamUrl(file.path);
            video.addEventListener('error', () => showPlayerError(stage, 'This video cannot be played in the browser. Its codec is not supported—commonly AV1 in a .mp4 from YouTube. Re-download it with a browser-friendly format (yt_dlp_format), or use Download.'));
            stage.appendChild(video);

            loadSubtitleTrack(file, video);
        }

        openOverlay('playerModal', { focus: '[data-close="playerModal"]' });
    }

    async function loadSubtitleTrack(file, video) {
        if (playerAbort) playerAbort.abort();
        playerAbort = new AbortController();
        try {
            const res = await apiFetch('/api/media/info?path=' + encodeURIComponent(file.path), { signal: playerAbort.signal });
            if (!res.ok) return;
            const info = await res.json();
            if (!info || !info.has_subtitles) return;
            if (!video.isConnected) return;
            const track = document.createElement('track');
            track.kind = 'subtitles';
            track.label = 'Subtitles';
            track.srclang = 'en';
            track.default = true;
            track.src = '/api/media/subtitles?path=' + encodeURIComponent(file.path);
            video.appendChild(track);
            if (video.textTracks && video.textTracks.length) {
                video.textTracks[0].mode = 'showing';
            }
        } catch (err) {
            /* subtitles are optional */
        }
    }

    function showPlayerError(stage, message) {
        clear(stage);
        const box = el('div', 'empty-state');
        const ico = el('span', 'empty-ico');
        ico.appendChild(icon('alert'));
        box.appendChild(ico);
        box.appendChild(el('strong', null, 'Cannot play this file'));
        box.appendChild(el('p', null, message));
        stage.appendChild(box);
    }
    /* ── Tasks ─────────────────────────────────────────────────────────── */

    const TASK_FILTERS = {
        all: () => true,
        active: (task) => task.status === 'running' || task.status === 'waiting',
        done: (task) => task.status === 'completed',
        failed: (task) => task.status === 'failed' || task.status === 'cancelled'
    };

    function activeTaskCount() {
        return state.tasks.filter(TASK_FILTERS.active).length;
    }

    function taskBadgeClass(status) {
        if (status === 'running') return 'badge b-run';
        if (status === 'completed') return 'badge b-done';
        if (status === 'failed') return 'badge b-fail';
        if (status === 'cancelled') return 'badge b-cancel';
        return 'badge b-wait';
    }

    function taskStatusLabel(status) {
        if (status === 'running') return 'Running';
        if (status === 'completed') return 'Done';
        if (status === 'failed') return 'Failed';
        if (status === 'cancelled') return 'Cancelled';
        return 'Queued';
    }

    function taskIcon(task) {
        const wrap = el('span', 'task-ico' + (task.status === 'running' ? ' is-running' : ''));
        wrap.appendChild(icon(task.type && task.type.indexOf('Download') >= 0 ? 'youtube' : 'terminal'));
        return wrap;
    }

    function taskLogsLast(task) {
        const logs = Array.isArray(task.logs) ? task.logs : [];
        return logs.length ? logs[logs.length - 1] : '';
    }

    async function updateTasks(force) {
        if (!state.authenticated) return;
        try {
            const res = await apiFetch('/api/tasks');
            if (!res.ok) throw new Error('Could not load tasks');
            const data = await res.json();
            const tasks = Array.isArray(data) ? data : [];
            // The log tail is part of the signature: the server caps logs at 50
            // entries, so counting them alone would stop detecting updates once
            // the cap is reached and the list would look frozen.
            const signature = tasks.map((task) => {
                const logs = Array.isArray(task.logs) ? task.logs : [];
                const tail = logs.length ? logs[logs.length - 1] : '';
                return task.id + ':' + task.status + ':' + logs.length + ':' + tail;
            }).join('|');
            const changed = signature !== state.tasksHash;

            detectTaskTransitions(tasks);
            state.tasks = tasks;
            state.tasksHash = signature;

            if (changed || force) {
                renderTasks();
            }
            noteRequestSuccess();
        } catch (err) {
            /* connectivity is reported centrally by apiFetch */
        }
    }

    function detectTaskTransitions(tasks) {
        const seen = state.taskStatus || {};
        const next = {};
        tasks.forEach((task) => {
            next[task.id] = task.status;
            const before = seen[task.id];
            if (before && before !== task.status) {
                if (task.status === 'completed') {
                    toast('Download finished: ' + shortTarget(task.target), 'success', {
                        label: 'Open media',
                        onClick: () => go('media')
                    });
                } else if (task.status === 'failed') {
                    toast('Download failed: ' + shortTarget(task.target), 'error', {
                        label: 'View logs',
                        onClick: () => { state.tasksFilter = 'failed'; go('tasks'); }
                    });
                }
            }
        });
        state.taskStatus = next;
    }

    function shortTarget(target) {
        if (!target) return 'task';
        try {
            const url = new URL(target);
            const tail = url.pathname.split('/').filter(Boolean).pop();
            return tail ? decodeURIComponent(tail) : url.hostname;
        } catch (err) {
            return target.length > 42 ? target.slice(0, 39) + '…' : target;
        }
    }

    function renderTasks() {
        const active = activeTaskCount();
        const badge = $('#navTaskBadge');
        if (badge) {
            badge.textContent = String(active);
            badge.hidden = active === 0;
            badge.classList.toggle('is-hot', active > 0);
        }

        setText('statTasks', String(active));
        setText('statTasksFoot', active === 0
            ? 'No background work'
            : active + (active === 1 ? ' task running' : ' tasks running'));

        renderTaskPips();
        renderTaskChips();
        renderOverviewTasks();
        renderTasksList();
        updateViewSub();
    }

    function renderTaskPips() {
        const container = $('#taskPips');
        if (!container) return;
        clear(container);
        const recent = state.tasks.slice(0, 8);
        for (let i = 0; i < 8; i += 1) {
            const pip = document.createElement('i');
            const task = recent[i];
            if (task) {
                if (task.status === 'running' || task.status === 'waiting') pip.className = 'is-running';
                else if (task.status === 'completed') pip.className = 'is-done';
                else if (task.status === 'failed') pip.className = 'is-failed';
            }
            container.appendChild(pip);
        }
    }

    function renderTaskChips() {
        const counts = {
            all: state.tasks.length,
            active: activeTaskCount(),
            done: state.tasks.filter(TASK_FILTERS.done).length,
            failed: state.tasks.filter(TASK_FILTERS.failed).length
        };
        Object.keys(counts).forEach((key) => {
            $$('[data-task-count="' + key + '"]').forEach((node) => { node.textContent = String(counts[key]); });
        });
        $$('#taskChips .chip').forEach((chip) => {
            const active = chip.dataset.taskFilter === state.tasksFilter;
            chip.classList.toggle('is-active', active);
            chip.setAttribute('aria-pressed', active ? 'true' : 'false');
        });
    }

    function renderOverviewTasks() {
        const container = $('#overviewTasks');
        if (!container) return;
        clear(container);

        setText('overviewTaskSub', activeTaskCount() + ' active · ' + state.tasks.length + ' total');

        const ordered = state.tasks.slice().sort((a, b) => {
            const rank = (task) => (TASK_FILTERS.active(task) ? 0 : 1);
            if (rank(a) !== rank(b)) return rank(a) - rank(b);
            return (Number(b.created_at) || 0) - (Number(a.created_at) || 0);
        }).slice(0, 4);

        if (ordered.length === 0) {
            container.appendChild(el('p', 'empty-inline', 'No background tasks'));
            return;
        }

        ordered.forEach((task) => {
            const row = el('div', 'mini-task');
            const ico = el('span', 'mini-ico');
            ico.appendChild(icon(task.type && task.type.indexOf('Download') >= 0 ? 'youtube' : 'terminal'));
            row.appendChild(ico);

            const info = el('div', 'mini-info');
            info.appendChild(el('strong', null, task.type || 'Task'));
            info.appendChild(el('span', null, shortTarget(task.target)));
            row.appendChild(info);

            row.appendChild(el('span', taskBadgeClass(task.status), taskStatusLabel(task.status)));
            container.appendChild(row);
        });
    }

    function renderTasksList() {
        const container = $('#taskListFull');
        if (!container) return;
        const openIds = new Set(state.openLogIds);

        // Remember log scroll positions so a re-render (which rebuilds the list)
        // does not yank the view: keep following the bottom if it was pinned
        // there, otherwise restore the previous offset.
        const scrollState = {};
        $$('.task-card', container).forEach((card) => {
            const pre = $('.task-logs', card);
            if (!pre) return;
            scrollState[card.dataset.taskId] = {
                top: pre.scrollTop,
                atBottom: pre.scrollHeight - pre.scrollTop - pre.clientHeight < 8
            };
        });

        clear(container);

        const filter = TASK_FILTERS[state.tasksFilter] || TASK_FILTERS.all;
        const tasks = state.tasks.filter(filter);

        const empty = $('#taskEmpty');
        if (empty) empty.hidden = tasks.length !== 0;
        setText('taskSummary', activeTaskCount() + ' active · ' + state.tasks.length + ' total');

        tasks.forEach((task) => {
            container.appendChild(buildTaskCard(task, openIds.has(task.id)));
        });

        $$('.task-card', container).forEach((card) => {
            const pre = $('.task-logs', card);
            if (!pre) return;
            const saved = scrollState[card.dataset.taskId];
            if (saved && !saved.atBottom) pre.scrollTop = saved.top;
            else pre.scrollTop = pre.scrollHeight;
        });
    }

    function buildTaskCard(task, logsOpen) {
        const card = el('article', 'task-card');
        card.dataset.taskId = task.id;

        const main = el('div', 'task-main');
        main.appendChild(taskIcon(task));

        const info = el('div', 'task-info');
        info.appendChild(el('strong', null, task.type || 'Task'));
        info.appendChild(el('span', 'task-target', task.target || '—'));
        main.appendChild(info);

        const side = el('div', 'task-side');
        side.appendChild(el('span', taskBadgeClass(task.status), taskStatusLabel(task.status)));
        side.appendChild(el('span', 'task-age', timeAgo(task.created_at)));
        main.appendChild(side);
        card.appendChild(main);

        const actions = el('div', 'task-actions');

        const logsButton = el('button', 'btn btn-ghost btn-sm', logsOpen ? 'Hide logs' : 'Show logs');
        logsButton.type = 'button';
        logsButton.addEventListener('click', () => toggleTaskLogs(task.id));
        actions.appendChild(logsButton);

        if (task.status === 'running' || task.status === 'waiting') {
            const cancel = el('button', 'btn btn-danger-soft btn-sm', 'Cancel');
            cancel.type = 'button';
            cancel.addEventListener('click', () => cancelTask(task));
            actions.appendChild(cancel);
        }

        if (task.status === 'failed' || task.status === 'cancelled') {
            const retry = el('button', 'btn btn-soft btn-sm', 'Retry');
            retry.type = 'button';
            retry.addEventListener('click', () => retryTask(task));
            actions.appendChild(retry);
        }

        card.appendChild(actions);

        if (logsOpen) {
            const logs = el('pre', 'task-logs');
            const lines = Array.isArray(task.logs) ? task.logs : [];
            logs.textContent = lines.length ? lines.join('\n') : 'No log output yet.';
            card.appendChild(logs);
        }

        return card;
    }

    function toggleTaskLogs(id) {
        if (state.openLogIds.has(id)) state.openLogIds.delete(id);
        else state.openLogIds.add(id);
        renderTasksList();
    }

    async function cancelTask(task) {
        try {
            const res = await apiFetch('/api/tasks', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ action: 'cancel', task_id: task.id })
            });
            if (!res.ok) {
                toast(await readError(res, 'Could not cancel the task.'), 'error');
                return;
            }
            toast('Task cancelled.', 'info');
            updateTasks(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            toast('Could not cancel the task.', 'error');
        }
    }

    async function retryTask(task) {
        try {
            const res = await apiFetch('/api/tasks', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ action: 'retry', task_id: task.id })
            });
            if (!res.ok) {
                const data = await res.json().catch(() => ({}));
                if (data && data.ytdlp_missing) {
                    openYtModal();
                    return;
                }
                toast((data && data.error) || 'Could not retry the task.', 'error');
                return;
            }
            toast('Task queued again.', 'success');
            updateTasks(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            toast('Could not retry the task.', 'error');
        }
    }

    /* ── yt-dlp fetch flow ─────────────────────────────────────────────── */

    let ytdlpInstalled = null;

    function openYtModal() {
        const feedback = $('#ytFeedback');
        const input = $('#ytUrlInput');
        if (feedback) feedback.textContent = '';
        if (input) input.value = '';
        openOverlay('ytModal', { focus: '#ytUrlInput' });
        refreshYtDlpStatus();
    }

    async function refreshYtDlpStatus() {
        const prompt = $('#ytdlpPromptBox');
        const form = $('#ytdlpReadyForm');
        const footer = $('#ytdlpModalFooter');
        try {
            const res = await apiFetch('/api/tasks/ytdlp-status');
            const data = await res.json();
            ytdlpInstalled = Boolean(data && data.installed);
        } catch (err) {
            ytdlpInstalled = null;
        }

        if (ytdlpInstalled === true) {
            if (prompt) prompt.hidden = true;
            if (form) form.hidden = false;
            if (footer) footer.hidden = false;
        } else if (ytdlpInstalled === false) {
            if (prompt) prompt.hidden = false;
            if (form) form.hidden = true;
            if (footer) footer.hidden = true;
            setText('ytdlpInstallStatus', '');
        } else {
            if (prompt) prompt.hidden = true;
            if (form) form.hidden = false;
            if (footer) footer.hidden = false;
        }
    }

    async function installYtDlp() {
        const button = $('#btnConfirmInstallYtDlp');
        const status = $('#ytdlpInstallStatus');
        if (button) button.disabled = true;
        if (status) status.textContent = 'Downloading the official build…';
        try {
            const res = await apiFetch('/api/tasks/install-ytdlp', { method: 'POST' });
            const data = await res.json().catch(() => ({}));
            if (!res.ok) {
                if (status) status.textContent = (data && data.error) || 'Installation failed.';
                toast((data && data.error) || 'yt-dlp installation failed.', 'error');
                return;
            }
            if (status) status.textContent = 'Installed. Ready to fetch.';
            toast('yt-dlp is installed and configured.', 'success');
            ytdlpInstalled = true;
            await refreshYtDlpStatus();
            const input = $('#ytUrlInput');
            if (input) input.focus();
        } catch (err) {
            if (handleAuthFailure(err)) return;
            if (status) status.textContent = 'The download could not be completed.';
            toast('yt-dlp installation failed.', 'error');
        } finally {
            if (button) button.disabled = false;
        }
    }

    async function submitFetch() {
        const input = $('#ytUrlInput');
        const feedback = $('#ytFeedback');
        const button = $('#btnSubmitYt');
        if (!input) return;
        const url = input.value.trim();
        if (feedback) feedback.textContent = '';

        if (!url) {
            if (feedback) feedback.textContent = 'Paste a media link first.';
            input.focus();
            return;
        }
        let parsed = null;
        try { parsed = new URL(url); } catch (err) { parsed = null; }
        if (!parsed || (parsed.protocol !== 'http:' && parsed.protocol !== 'https:')) {
            if (feedback) feedback.textContent = 'Enter a valid http or https link.';
            input.focus();
            return;
        }

        if (button) button.disabled = true;
        try {
            const res = await apiFetch('/api/tasks', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ action: 'add', url: url })
            });
            const data = await res.json().catch(() => ({}));
            if (!res.ok) {
                if (data && data.ytdlp_missing) {
                    ytdlpInstalled = false;
                    await refreshYtDlpStatus();
                    if (feedback) feedback.textContent = '';
                    return;
                }
                if (feedback) feedback.textContent = (data && data.error) || 'Could not queue the download.';
                return;
            }
            input.value = '';
            closeOverlay('ytModal');
            toast('Download queued. Track it in Tasks.', 'success', {
                label: 'View tasks',
                onClick: () => go('tasks')
            });
            updateTasks(true);
        } catch (err) {
            if (handleAuthFailure(err)) return;
            if (feedback) feedback.textContent = err.message || 'Could not queue the download.';
        } finally {
            if (button) button.disabled = false;
        }
    }
    /* ── Overview ─────────────────────────────────────────────────────── */

    async function loadOverview(force) {
        if (!state.authenticated) return;
        const tasks = [updateStats(), updateTasks(false), loadOverviewFiles(), loadLibraries(force)];
        await Promise.allSettled(tasks);
    }

    async function loadOverviewFiles() {
        // "Recent" spans the whole cloud, not just the storage root: uploads and
        // fetched media live under media/uploads and media/yt_library, so a
        // root-only listing showed an empty panel even after uploading.
        const sources = ['', LIBRARY_PATHS.uploads, LIBRARY_PATHS.youtube];
        try {
            const results = await Promise.allSettled(sources.map((path) => fetchDir(path)));

            let rootFiles = [];
            const files = [];
            results.forEach((result, index) => {
                if (result.status !== 'fulfilled') return;
                const list = Array.isArray(result.value.files) ? result.value.files : [];
                if (index === 0) rootFiles = list;
                list.forEach((file) => {
                    if (!file.is_dir) files.push(file);
                });
            });

            if (!results.some((result) => result.status === 'fulfilled')) {
                throw (results[0] && results[0].reason) || new Error('Could not list files');
            }

            let total = 0;
            rootFiles.forEach((file) => {
                if (!file.is_dir) total += Number(file.size) || 0;
            });
            state.rootOtherSize = total;
            state.rootOtherSizeAt = Date.now();

            files.sort((a, b) => (Number(b.mod_time) || 0) - (Number(a.mod_time) || 0));
            state.recentFiles = files;
            renderRecent(files);
            renderStorage();
            noteRequestSuccess();
        } catch (err) {
            if (handleAuthFailure(err)) return;
            const container = $('#recentFiles');
            if (container) {
                clear(container);
                container.appendChild(el('p', 'empty-inline', 'Could not list recent files.'));
            }
        }
    }

    /* ── Command palette ───────────────────────────────────────────────── */

    function paletteSource() {
        const items = [];
        const nav = [
            ['overview', 'Overview', 'home'],
            ['files', 'Files', 'folder'],
            ['media', 'Media', 'film'],
            ['tasks', 'Tasks', 'terminal'],
            ['settings', 'Settings', 'sliders']
        ];
        nav.forEach(([view, label, iconName]) => {
            items.push({ group: 'Navigate', label: label, sub: 'Go to the ' + label.toLowerCase() + ' view', icon: iconName, run: () => go(view) });
        });

        items.push({ group: 'Actions', label: 'Upload files', sub: 'Open the file picker', icon: 'upload', run: () => { go('files'); setTimeout(openUploadPicker, 120); } });
        items.push({ group: 'Actions', label: 'Fetch media', sub: 'Queue a yt-dlp download', icon: 'youtube', run: openYtModal });
        items.push({ group: 'Actions', label: 'New folder', sub: 'Create a folder in the current path', icon: 'plus', run: () => { go('files'); setTimeout(openMkdir, 120); } });
        items.push({ group: 'Actions', label: 'Toggle theme', sub: 'Switch between light and dark', icon: 'sun', run: cycleTheme });
        items.push({ group: 'Actions', label: 'Refresh current view', sub: 'Reload data from the server', icon: 'refresh', run: refreshCurrentView });
        items.push({ group: 'Actions', label: 'Sign out', sub: 'End this browser session', icon: 'logout', run: logout });

        state.files.forEach((file) => {
            items.push({
                group: 'Files',
                label: file.name,
                sub: (file.is_dir ? 'Folder · ' : (file.human_size || formatBytes(file.size)) + ' · ') + file.path,
                icon: kindIcon(fileKind(file)),
                run: () => {
                    if (file.is_dir) {
                        go('files');
                        state.filesLoaded = false;
                        setTimeout(() => loadFiles(file.path), 80);
                    } else if (isMediaFile(file)) {
                        openPlayer(file);
                    } else {
                        const anchor = document.createElement('a');
                        anchor.href = downloadUrl(file.path);
                        anchor.download = file.name;
                        document.body.appendChild(anchor);
                        anchor.click();
                        anchor.remove();
                    }
                }
            });
        });

        return items;
    }

    function refreshCurrentView() {
        if (state.view === 'overview') loadOverview(true);
        else if (state.view === 'files') loadFiles(state.path, { silent: true });
        else if (state.view === 'media') loadMedia(true);
        else if (state.view === 'tasks') updateTasks(true);
        else renderSettings();
    }

    function openPalette() {
        state.palette.open = true;
        state.palette.index = 0;
        const input = $('#paletteInput');
        if (input) input.value = '';
        openOverlay('paletteModal', { focus: '#paletteInput' });
        renderPalette('');
    }

    function closePalette() {
        state.palette.open = false;
        closeOverlay('paletteModal');
    }

    function togglePalette() {
        if (state.palette.open) closePalette();
        else openPalette();
    }

    function filteredPaletteItems(query) {
        const source = paletteSource();
        const needle = query.trim().toLowerCase();
        if (!needle) return source.slice(0, 30);
        return source.filter((item) => {
            return item.label.toLowerCase().indexOf(needle) >= 0 || (item.sub || '').toLowerCase().indexOf(needle) >= 0;
        }).slice(0, 40);
    }

    function renderPalette(query) {
        state.palette.items = filteredPaletteItems(query);
        if (state.palette.index >= state.palette.items.length) state.palette.index = Math.max(0, state.palette.items.length - 1);

        const container = $('#paletteResults');
        if (!container) return;
        clear(container);

        if (state.palette.items.length === 0) {
            container.appendChild(el('p', 'palette-empty', 'No matches. Try a different word.'));
            return;
        }

        let lastGroup = null;
        state.palette.items.forEach((item, index) => {
            if (item.group !== lastGroup) {
                lastGroup = item.group;
                container.appendChild(el('p', 'palette-group', item.group));
            }
            const node = el('div', 'palette-item' + (index === state.palette.index ? ' is-selected' : ''));
            node.setAttribute('role', 'option');
            node.setAttribute('aria-selected', index === state.palette.index ? 'true' : 'false');
            const ico = el('span', 'palette-ico');
            ico.appendChild(icon(item.icon));
            node.appendChild(ico);
            const label = el('span', 'palette-label');
            label.appendChild(el('strong', null, item.label));
            label.appendChild(el('span', null, item.sub || ''));
            node.appendChild(label);
            node.addEventListener('mouseenter', () => {
                if (state.palette.index === index) return;
                state.palette.index = index;
                updatePaletteSelection();
            });
            node.addEventListener('click', () => runPaletteItem(index));
            container.appendChild(node);
        });
        updatePaletteSelection();
    }

    function updatePaletteSelection() {
        const container = $('#paletteResults');
        if (!container) return;
        const nodes = $$('.palette-item', container);
        nodes.forEach((node, index) => {
            const selected = index === state.palette.index;
            node.classList.toggle('is-selected', selected);
            node.setAttribute('aria-selected', selected ? 'true' : 'false');
            if (selected && typeof node.scrollIntoView === 'function') {
                node.scrollIntoView({ block: 'nearest' });
            }
        });
    }

    function movePalette(delta) {
        if (state.palette.items.length === 0) return;
        const count = state.palette.items.length;
        state.palette.index = (state.palette.index + delta + count) % count;
        updatePaletteSelection();
    }

    function runPaletteItem(index) {
        const item = state.palette.items[index];
        if (!item) return;
        closePalette();
        item.run();
    }

    /* ── Settings ──────────────────────────────────────────────────────── */

    function renderSettings() {
        applyTheme(state.themePref, false);
        applyMotion(state.motionPref, false);

        const version = state.status && state.status.version ? state.status.version : '2.0.0';
        setText('setEndpoint', location.host || 'localhost');
        setText('setVersion', 'v' + version);
        const tls = Boolean(state.status && state.status.tls_enabled);
        setText('setProtocol', (location.protocol || 'http:').replace(':', '').toUpperCase() + (tls ? ' · TLS' : ' · plain'));
        setText('setSession', formatServerStart());
    }

    function resetPreferences() {
        writePref('pycloud.theme', 'graphite');
        writePref('pycloud.motion', 'full');
        applyTheme('graphite', true);
        applyMotion('full', true);
        toast('Local preferences reset.', 'info');
    }

    /* ── Clock ─────────────────────────────────────────────────────────── */

    let clockTimer = null;

    function updateClock() {
        const now = new Date();
        const pad = (n) => String(n).padStart(2, '0');
        const hhmm = pad(now.getHours()) + ':' + pad(now.getMinutes());
        setText('localClock', hhmm);

        const elapsed = serverUptimeNow();
        if (elapsed === null) {
            setText('sessionUptime', 'up —');
            setText('serverUptime', '—');
            setText('setUptime', '—');
        } else {
            setText('sessionUptime', 'up ' + formatUptimeLong(elapsed));
            setText('serverUptime', formatUptimeCompact(elapsed));
            setText('setUptime', formatUptimeLong(elapsed));
        }

        const hour = now.getHours();
        const part = hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening';
        setText('greeting', 'Good ' + part + ', admin');
    }

    function startClock() {
        updateClock();
        if (clockTimer) return;
        clockTimer = setInterval(updateClock, 1000);
    }

    /* ── Polling ───────────────────────────────────────────────────────── */

    function startPolling() {
        stopPolling();
        state.pollStats = setInterval(() => {
            if (document.hidden || state.statsInFlight) return;
            state.statsInFlight = true;
            updateStats().finally(() => { state.statsInFlight = false; });
        }, 5000);
        state.pollTasks = setInterval(() => {
            // Never stack requests: if one is still in flight, skip this tick.
            if (document.hidden || state.tasksInFlight) return;
            state.tasksInFlight = true;
            updateTasks(false).finally(() => { state.tasksInFlight = false; });
        }, 2600);
    }

    function stopPolling() {
        if (state.pollStats) { clearInterval(state.pollStats); state.pollStats = null; }
        if (state.pollTasks) { clearInterval(state.pollTasks); state.pollTasks = null; }
    }

    /* ── Bootstrap ─────────────────────────────────────────────────────── */

    async function initApp() {
        state.authenticated = true;
        updateServerChrome();
        startClock();
        renderView(currentHashView());
        startPolling();
        loadLibraries(false);
    }

    /* ── Wiring ────────────────────────────────────────────────────────── */

    function wireConfirm() {
        const ok = $('#btnConfirmOk');
        const cancel = $('#btnConfirmCancel');
        const input = $('#confirmInput');
        if (ok) ok.addEventListener('click', () => {
            if (state.confirmExpected && (!input || input.value.trim() !== state.confirmExpected)) {
                if (input) { input.focus(); input.select(); }
                return;
            }
            settleConfirm(true);
        });
        if (cancel) cancel.addEventListener('click', () => settleConfirm(false));
        if (input) input.addEventListener('input', () => {
            if (ok) ok.disabled = Boolean(state.confirmExpected) && input.value.trim() !== state.confirmExpected;
        });
        if (input) input.addEventListener('keydown', (event) => {
            if (event.key === 'Enter') {
                event.preventDefault();
                if (ok && !ok.disabled) ok.click();
            }
        });
    }

    function wireOverlayDismiss() {
        $$('.overlay').forEach((overlay) => {
            overlay.addEventListener('mousedown', (event) => {
                if (event.target !== overlay) return;
                if (overlay.classList.contains('auth-overlay')) return;
                if (overlay.id === 'playerModal') return;
                closeOverlay(overlay.id);
            });
        });
        $$('[data-close]').forEach((button) => {
            button.addEventListener('click', () => closeOverlay(button.dataset.close));
        });
    }

    function wireNavigation() {
        $$('[data-nav]').forEach((link) => {
            link.addEventListener('click', () => closeSidebar());
        });
        $$('[data-library]').forEach((button) => {
            button.addEventListener('click', () => openLibrary(button.dataset.library));
        });
        $$('#categoryChips .chip').forEach((chip) => {
            chip.addEventListener('click', () => {
                const category = chip.dataset.cat;
                const path = category === 'all' ? '' : LIBRARY_PATHS[category];
                if (path === undefined) return;
                state.category = category;
                state.filesLoaded = false;
                loadFiles(path);
            });
        });
        $$('#mediaChips .chip').forEach((chip) => {
            chip.addEventListener('click', () => {
                state.mediaFilter = chip.dataset.mediaFilter;
                renderMedia();
            });
        });
        $$('#taskChips .chip').forEach((chip) => {
            chip.addEventListener('click', () => {
                state.tasksFilter = chip.dataset.taskFilter;
                renderTasks();
            });
        });
    }

    function wireFiles() {
        const search = $('#searchInput');
        if (search) {
            const debounced = debounce(applyFileFilter, 120);
            search.addEventListener('input', () => { renderSearchClear(); debounced(); });
            search.addEventListener('keydown', (event) => {
                if (event.key === 'Escape') {
                    search.value = '';
                    applyFileFilter();
                }
            });
        }
        const clear = $('#btnClearSearch');
        if (clear) clear.addEventListener('click', () => {
            if (search) search.value = '';
            applyFileFilter();
            if (search) search.focus();
        });

        const sort = $('#sortSelect');
        if (sort) sort.addEventListener('change', () => {
            state.sort = sort.value;
            applyFileFilter();
        });

        const zone = $('#dropZone');
        const input = $('#fileInput');
        if (zone) {
            zone.addEventListener('dragover', (event) => {
                event.preventDefault();
                zone.classList.add('is-dragover');
            });
            zone.addEventListener('dragleave', (event) => {
                if (event.target === zone) zone.classList.remove('is-dragover');
            });
            zone.addEventListener('drop', handleDrop);
        }
        if (input) input.addEventListener('change', handleFileInput);

        const browse = $('#browseBtn');
        if (browse) browse.addEventListener('click', openUploadPicker);

        const cancelUploadButton = $('#btnCancelUpload');
        if (cancelUploadButton) cancelUploadButton.addEventListener('click', cancelUpload);
    }

    function renderSearchClear() {
        const search = $('#searchInput');
        const clear = $('#btnClearSearch');
        if (clear) clear.hidden = !search || search.value.length === 0;
    }

    function wireTasks() {
        const fetchButtons = ['#btnFetchVideo', '#btnTasksFetch', '#btnEmptyFetch', '#qaFetch', '#btnMediaFetch'];
        fetchButtons.forEach((selector) => {
            const button = $(selector);
            if (button) button.addEventListener('click', openYtModal);
        });
        const install = $('#btnConfirmInstallYtDlp');
        if (install) install.addEventListener('click', installYtDlp);
        const submit = $('#btnSubmitYt');
        if (submit) submit.addEventListener('click', submitFetch);
        const ytForm = $('#ytdlpReadyForm');
        if (ytForm) ytForm.addEventListener('submit', (event) => { event.preventDefault(); submitFetch(); });
        const ytInput = $('#ytUrlInput');
        if (ytInput) ytInput.addEventListener('keydown', (event) => {
            if (event.key === 'Enter') { event.preventDefault(); submitFetch(); }
        });
    }

    function wireSettings() {
        $$('#themePicker [data-theme-opt]').forEach((button) => {
            button.addEventListener('click', () => {
                applyTheme(button.dataset.themeOpt, true);
                toast('Theme set to ' + button.querySelector('.theme-name').textContent + '.', 'info');
            });
        });
        const motion = $('#prefMotion');
        if (motion) motion.addEventListener('change', () => {
            applyMotion(motion.checked ? 'reduced' : 'full', true);
        });
        const reset = $('#btnResetPrefs');
        if (reset) reset.addEventListener('click', resetPreferences);
        const logoutButtons = ['#btnLogout', '#btnSettingsLogout'];
        logoutButtons.forEach((selector) => {
            const button = $(selector);
            if (button) button.addEventListener('click', logout);
        });
    }

    function wireForms() {
        const renameForm = $('#renameForm');
        if (renameForm) renameForm.addEventListener('submit', submitRename);
        const mkdirForm = $('#mkdirForm');
        if (mkdirForm) mkdirForm.addEventListener('submit', submitMkdir);
        const loginForm = $('#loginForm');
        if (loginForm) loginForm.addEventListener('submit', submitLogin);
        const setupForm = $('#setupForm');
        if (setupForm) setupForm.addEventListener('submit', submitSetup);
    }

    function wireTopbar() {
        const menu = $('#btnMenu');
        if (menu) menu.addEventListener('click', openSidebar);
        const close = $('#btnSidebarClose');
        if (close) close.addEventListener('click', closeSidebar);
        const scrim = $('#sidebarScrim');
        if (scrim) scrim.addEventListener('click', closeSidebar);
        const theme = $('#btnTheme');
        if (theme) theme.addEventListener('click', cycleTheme);
        const paletteButton = $('#btnPalette');
        if (paletteButton) paletteButton.addEventListener('click', openPalette);
        const uploadTop = $('#btnUploadTop');
        if (uploadTop) uploadTop.addEventListener('click', openUploadPicker);
        const quickUpload = $('#qaUpload');
        if (quickUpload) quickUpload.addEventListener('click', () => { go('files'); setTimeout(openUploadPicker, 140); });
        const newFolder = $('#btnNewFolder');
        if (newFolder) newFolder.addEventListener('click', () => {
            if (isTrashPath(state.path)) { toast('Trash is read-only. Open another folder first.', 'error'); return; }
            openMkdir();
        });
        const emptyTrash = $('#btnEmptyTrash');
        if (emptyTrash) emptyTrash.addEventListener('click', clearTrash);
        const retry = $('#btnRetryConn');
        if (retry) retry.addEventListener('click', async () => {
            // Probe the server directly — that is what actually sets/clears the banner.
            if (await checkServerStatus()) {
                refreshCurrentView();
                updateStats();
                updateTasks(true);
            }
        });
        const refreshOverview = $('#btnRefreshOverview');
        if (refreshOverview) refreshOverview.addEventListener('click', () => loadOverview(true));
        const refreshMedia = $('#btnRefreshMedia');
        if (refreshMedia) refreshMedia.addEventListener('click', () => loadMedia(true));
    }

    function wirePalette() {
        const input = $('#paletteInput');
        if (input) {
            input.addEventListener('input', () => {
                state.palette.index = 0;
                renderPalette(input.value);
            });
            input.addEventListener('keydown', (event) => {
                if (event.key === 'ArrowDown') { event.preventDefault(); movePalette(1); }
                else if (event.key === 'ArrowUp') { event.preventDefault(); movePalette(-1); }
                else if (event.key === 'Enter') { event.preventDefault(); runPaletteItem(state.palette.index); }
                else if (event.key === 'Escape') { event.preventDefault(); closePalette(); }
            });
        }
    }

    function wireKeyboard() {
        document.addEventListener('keydown', (event) => {
            const mod = event.metaKey || event.ctrlKey;
            if (mod && (event.key === 'k' || event.key === 'K')) {
                event.preventDefault();
                togglePalette();
                return;
            }
            if (event.key === 'Escape') {
                if (state.palette.open) { closePalette(); return; }
                if (document.body.classList.contains('sidebar-open')) { closeSidebar(); return; }
                closeTopOverlay();
            }
        });
    }

    function wireHashChange() {
        window.addEventListener('hashchange', () => {
            renderView(currentHashView());
        });
        window.addEventListener('resize', () => {
            if (window.innerWidth > 900) closeSidebar();
        });
        document.addEventListener('visibilitychange', () => {
            if (document.hidden || !state.authenticated) return;
            updateTasks(false);
            updateStats();
        });
    }

    function wireThemeWatcher() {
        if (!window.matchMedia) return;
        const query = window.matchMedia('(prefers-color-scheme: light)');
        const handler = () => {
            if (state.themePref === 'system') applyTheme('system', false);
        };
        if (typeof query.addEventListener === 'function') query.addEventListener('change', handler);
        else if (typeof query.addListener === 'function') query.addListener(handler);
    }

    function wire() {
        applyTheme(state.themePref, false);
        applyMotion(state.motionPref, false);

        wireOverlayDismiss();
        wireConfirm();
        wireNavigation();
        wireFiles();
        wireTasks();
        wireSettings();
        wireForms();
        wireTopbar();
        wirePalette();
        wireKeyboard();
        wireHashChange();
        wireThemeWatcher();
    }

    async function boot() {
        wire();
        const ready = await checkServerStatus();
        if (ready) {
            await initApp();
        } else if (state.status && state.status.setup_needed) {
            startClock();
        } else {
            startClock();
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', boot);
    } else {
        boot();
    }

})();
