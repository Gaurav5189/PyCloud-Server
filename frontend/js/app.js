/**
 * PyCloud v2.0 - Secure SPA Client Engine
 * Fully defensive against XSS: strictly utilizes safe DOM creation and textContent.
 */

(function () {
    'use strict';

    // State Management
    const state = {
        currentPath: '',
        currentCategory: 'all', // 'all', 'uploads', 'youtube', 'trash'
        files: [],
        filteredFiles: [],
        tasks: [],
        activeUploadXHR: null,
        openLogIds: new Set(),
        pollIntervalStats: null,
        pollIntervalTasks: null,
    };

    // Helper: Read CSRF cookie
    function getCSRFToken() {
        const match = document.cookie.match(/(^|;)\s*pycloud_csrf=([^;]+)/);
        return match ? decodeURIComponent(match[2]) : '';
    }

    // Helper: Safe Fetch wrapper with CSRF injection
    async function apiFetch(url, options = {}) {
        options.headers = options.headers || {};
        
        // Attach anti-CSRF token
        const csrf = getCSRFToken();
        if (csrf) {
            options.headers['X-CSRF-Token'] = csrf;
        }

        const res = await fetch(url, options);

        if (res.status === 401) {
            showModal('loginModal');
            throw new Error('Authentication required');
        }

        if (res.status === 428) {
            showModal('setupModal');
            throw new Error('Initial setup required');
        }

        return res;
    }

    // Toast Notifications
    function showToast(message, type = 'info') {
        const container = document.getElementById('toastContainer');
        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;
        toast.textContent = message;

        container.appendChild(toast);
        setTimeout(() => {
            toast.style.opacity = '0';
            setTimeout(() => toast.remove(), 300);
        }, 3500);
    }

    // Modal Manager
    function showModal(id) {
        const modal = document.getElementById(id);
        if (modal) {
            modal.style.display = 'flex';
            const input = modal.querySelector('input');
            if (input) setTimeout(() => input.focus(), 50);
        }
    }

    function closeModal(id) {
        const modal = document.getElementById(id);
        if (modal) {
            modal.style.display = 'none';
            // Clear inputs and feedback
            modal.querySelectorAll('.feedback-msg').forEach(el => el.textContent = '');
            modal.querySelectorAll('input').forEach(el => {
                if (el.type !== 'hidden') el.value = '';
            });
            // Stop media if cinema modal is closed
            if (id === 'cinemaModal') {
                const content = document.getElementById('cinemaContent');
                content.textContent = '';
            }
        }
    }

    // Global modal close handlers
    document.querySelectorAll('[data-close-modal]').forEach(btn => {
        btn.addEventListener('click', () => {
            closeModal(btn.getAttribute('data-close-modal'));
        });
    });

    window.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            document.querySelectorAll('.modal-overlay').forEach(modal => {
                // Do not dismiss auth or setup modals with Escape
                if (!modal.classList.contains('auth-modal')) {
                    modal.style.display = 'none';
                }
            });
            const content = document.getElementById('cinemaContent');
            content.textContent = '';
        }
    });

    // 1. Initial Status Check
    async function checkServerStatus() {
        try {
            const res = await fetch('/api/status');
            const data = await res.json();

            if (data.setup_needed) {
                showModal('setupModal');
                return false;
            } else if (!data.logged_in) {
                showModal('loginModal');
                return false;
            }

            return true;
        } catch (err) {
            console.error('Status check error:', err);
            return false;
        }
    }

    // 2. Authentication: Setup & Login Handlers
    document.getElementById('btnSubmitSetup').addEventListener('click', async () => {
        const pass = document.getElementById('setupPassword').value;
        const confirm = document.getElementById('setupConfirmPassword').value;
        const feedback = document.getElementById('setupFeedback');

        if (pass.length < 8) {
            feedback.textContent = 'Password must be at least 8 characters long.';
            return;
        }
        if (pass !== confirm) {
            feedback.textContent = 'Passwords do not match.';
            return;
        }

        try {
            const res = await apiFetch('/api/setup', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ password: pass, confirm_password: confirm })
            });

            if (!res.ok) {
                const err = await res.json();
                feedback.textContent = err.error || 'Setup failed';
                return;
            }

            closeModal('setupModal');
            showToast('Setup completed successfully! Welcome to PyCloud.', 'success');
            initApp();
        } catch (err) {
            feedback.textContent = err.message || 'Setup request failed';
        }
    });

    document.getElementById('btnSubmitLogin').addEventListener('click', async () => {
        const pass = document.getElementById('loginPassword').value;
        const feedback = document.getElementById('loginFeedback');

        if (!pass) {
            feedback.textContent = 'Please enter your password.';
            return;
        }

        try {
            const res = await apiFetch('/api/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ password: pass })
            });

            if (!res.ok) {
                const err = await res.json();
                feedback.textContent = err.error || 'Authentication failed';
                return;
            }

            closeModal('loginModal');
            showToast('Authenticated successfully.', 'success');
            initApp();
        } catch (err) {
            feedback.textContent = err.message || 'Login request failed';
        }
    });

    document.getElementById('loginPassword').addEventListener('keydown', (e) => {
        if (e.key === 'Enter') document.getElementById('btnSubmitLogin').click();
    });

    document.getElementById('btnLogout').addEventListener('click', async () => {
        try {
            await apiFetch('/api/logout', { method: 'POST' });
            showToast('Logged out.', 'info');
            setTimeout(() => window.location.reload(), 300);
        } catch (err) {
            window.location.reload();
        }
    });

    // 3. System Stats Monitoring
    async function updateStats() {
        try {
            const res = await apiFetch('/api/stats');
            if (!res.ok) return;
            const data = await res.json();

            document.getElementById('statCpu').textContent = data.cpu_load || 'N/A';
            document.getElementById('statRam').textContent = data.ram_usage || 'N/A';
            document.getElementById('statDisk').textContent = data.free_gb || 'N/A';
        } catch (err) {
            // Silently ignore stat polling errors if logged out
        }
    }

    // 4. Task Manager Engine UI
    document.getElementById('taskHeaderToggle').addEventListener('click', () => {
        const list = document.getElementById('taskList');
        const icon = document.getElementById('btnToggleTasks');
        if (list.style.display === 'none') {
            list.style.display = 'block';
            icon.textContent = '▲';
        } else {
            list.style.display = 'none';
            icon.textContent = '▼';
        }
    });

    async function updateTasks() {
        try {
            const res = await apiFetch('/api/tasks');
            if (!res.ok) return;
            const tasks = await res.json();
            state.tasks = tasks || [];

            const activeCount = state.tasks.filter(t => t.status === 'running' || t.status === 'waiting').length;
            document.getElementById('activeTasksCount').textContent = activeCount;

            renderTasksList();
        } catch (err) {
            // Ignore background error
        }
    }

    function renderTasksList() {
        const container = document.getElementById('taskList');
        container.textContent = ''; // Safe wipe

        if (state.tasks.length === 0) {
            const empty = document.createElement('div');
            empty.className = 'empty-state';
            empty.textContent = 'No background tasks';
            container.appendChild(empty);
            return;
        }

        state.tasks.forEach(task => {
            const item = document.createElement('div');
            item.className = 'task-item';

            const row = document.createElement('div');
            row.className = 'task-row';

            const info = document.createElement('div');
            info.className = 'task-info';

            const badge = document.createElement('span');
            badge.className = 'task-type-badge';
            badge.textContent = task.type;

            const target = document.createElement('span');
            target.className = 'task-target';
            target.textContent = task.target;
            target.title = task.target;

            info.appendChild(badge);
            info.appendChild(target);

            const status = document.createElement('span');
            status.className = `task-status-tag status-${task.status}`;
            status.textContent = task.status;

            const actions = document.createElement('div');
            actions.className = 'task-actions';

            if (task.status === 'running' || task.status === 'waiting') {
                const cancelBtn = document.createElement('button');
                cancelBtn.className = 'btn btn-sm btn-danger';
                cancelBtn.textContent = 'Cancel';
                cancelBtn.onclick = () => taskAction('cancel', task.id);
                actions.appendChild(cancelBtn);
            }

            if (task.status === 'failed' || task.status === 'cancelled') {
                const retryBtn = document.createElement('button');
                retryBtn.className = 'btn btn-sm btn-secondary';
                retryBtn.textContent = 'Retry';
                retryBtn.onclick = () => taskAction('retry', task.id);
                actions.appendChild(retryBtn);
            }

            const logsBtn = document.createElement('button');
            logsBtn.className = 'btn btn-sm btn-outline';
            logsBtn.textContent = 'Logs';
            logsBtn.style.marginLeft = '4px';
            logsBtn.onclick = () => toggleTaskLogs(task.id);
            actions.appendChild(logsBtn);

            row.appendChild(info);
            row.appendChild(status);
            row.appendChild(actions);
            item.appendChild(row);

            // Task Logs section
            if (state.openLogIds.has(task.id) && task.logs && task.logs.length > 0) {
                const logBox = document.createElement('div');
                logBox.className = 'task-logs';
                logBox.textContent = task.logs.join('\n');
                item.appendChild(logBox);
            }

            container.appendChild(item);
        });
    }

    function toggleTaskLogs(id) {
        if (state.openLogIds.has(id)) {
            state.openLogIds.delete(id);
        } else {
            state.openLogIds.add(id);
        }
        renderTasksList();
    }

    async function taskAction(action, taskID) {
        try {
            await apiFetch('/api/tasks', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ action: action, task_id: taskID })
            });
            updateTasks();
        } catch (err) {
            showToast('Task action failed: ' + err.message, 'error');
        }
    }

    // Media Downloader Modal & yt-dlp Auto-Config
    async function checkYtDlpStatus() {
        try {
            const res = await apiFetch('/api/tasks/ytdlp-status');
            if (res.ok) {
                const data = await res.json();
                return data.installed === true;
            }
        } catch (err) {
            console.error('Failed to check yt-dlp status', err);
        }
        return false;
    }

    document.getElementById('btnFetchVideo').addEventListener('click', async () => {
        const promptBox = document.getElementById('ytdlpPromptBox');
        const readyForm = document.getElementById('ytdlpReadyForm');
        const footer = document.getElementById('ytdlpModalFooter');
        const installStatus = document.getElementById('ytdlpInstallStatus');
        const installBtn = document.getElementById('btnConfirmInstallYtDlp');

        installStatus.textContent = '';
        installBtn.disabled = false;
        installBtn.innerHTML = '<span>⬇️</span> Yes, Download & Configure';

        showModal('ytModal');

        // Check if yt-dlp is installed
        const isInstalled = await checkYtDlpStatus();
        if (isInstalled) {
            promptBox.style.display = 'none';
            readyForm.style.display = 'block';
            footer.style.display = 'flex';
        } else {
            promptBox.style.display = 'flex';
            readyForm.style.display = 'none';
            footer.style.display = 'none';
        }
    });

    document.getElementById('btnConfirmInstallYtDlp').addEventListener('click', async () => {
        const installBtn = document.getElementById('btnConfirmInstallYtDlp');
        const installStatus = document.getElementById('ytdlpInstallStatus');
        const promptBox = document.getElementById('ytdlpPromptBox');
        const readyForm = document.getElementById('ytdlpReadyForm');
        const footer = document.getElementById('ytdlpModalFooter');

        installBtn.disabled = true;
        installBtn.textContent = '⏳ Downloading & Configuring yt-dlp...';
        installStatus.className = 'install-status';
        installStatus.textContent = 'Fetching official release from GitHub for your system...';

        try {
            const res = await apiFetch('/api/tasks/install-ytdlp', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' }
            });

            const data = await res.json();
            if (!res.ok) {
                throw new Error(data.error || 'Failed to download yt-dlp');
            }

            installStatus.style.color = 'var(--success)';
            installStatus.textContent = '✅ yt-dlp downloaded and configured successfully!';
            showToast('yt-dlp is ready! You can now download media.', 'success');

            setTimeout(() => {
                promptBox.style.display = 'none';
                readyForm.style.display = 'block';
                footer.style.display = 'flex';
                document.getElementById('ytUrlInput').focus();
            }, 1000);
        } catch (err) {
            installBtn.disabled = false;
            installBtn.innerHTML = '<span>🔄</span> Try Again';
            installStatus.style.color = 'var(--danger)';
            installStatus.textContent = 'Error: ' + err.message;
        }
    });

    document.getElementById('btnSubmitYt').addEventListener('click', async () => {
        const url = document.getElementById('ytUrlInput').value.trim();
        const feedback = document.getElementById('ytFeedback');

        if (!url) {
            feedback.textContent = 'Please enter a valid media link.';
            return;
        }

        feedback.textContent = '';

        try {
            const res = await apiFetch('/api/tasks', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ action: 'add', url: url })
            });

            const data = await res.json();
            if (!res.ok) {
                if (data.ytdlp_missing) {
                    // Switch to confirmation prompt box
                    document.getElementById('ytdlpPromptBox').style.display = 'flex';
                    document.getElementById('ytdlpReadyForm').style.display = 'none';
                    document.getElementById('ytdlpModalFooter').style.display = 'none';
                    return;
                }
                feedback.textContent = data.error || 'Failed to queue task.';
                return;
            }

            closeModal('ytModal');
            showToast('Download task added to queue!', 'success');
            // Expand tasks widget to show progress
            document.getElementById('taskList').style.display = 'block';
            document.getElementById('btnToggleTasks').textContent = '▲';
            updateTasks();
        } catch (err) {
            feedback.textContent = err.message || 'Request failed';
        }
    });

    // 5. File Manager & Directory Listing
    async function loadFiles(path = '') {
        state.currentPath = path;
        const listContainer = document.getElementById('filesList');
        listContainer.textContent = '';

        const loading = document.createElement('div');
        loading.className = 'loading-state';
        loading.textContent = 'Loading files...';
        listContainer.appendChild(loading);

        try {
            const res = await apiFetch(`/api/files?path=${encodeURIComponent(path)}`);
            if (!res.ok) {
                throw new Error('Failed to load files');
            }

            const data = await res.json();
            state.files = data.files || [];
            updateBreadcrumbs(data.current_path || '');
            filterAndRenderFiles();
        } catch (err) {
            listContainer.textContent = '';
            const errDiv = document.createElement('div');
            errDiv.className = 'empty-state';
            errDiv.textContent = 'Error loading directory: ' + err.message;
            listContainer.appendChild(errDiv);
        }
    }

    function updateBreadcrumbs(relPath) {
        const container = document.getElementById('breadcrumbs');
        container.textContent = '';

        const rootCrumb = document.createElement('span');
        rootCrumb.className = 'crumb' + (relPath === '' ? ' active' : '');
        rootCrumb.textContent = '🏠 Root';
        rootCrumb.onclick = () => loadFiles('');
        container.appendChild(rootCrumb);

        if (!relPath) return;

        const parts = relPath.split('/').filter(Boolean);
        let accum = '';
        parts.forEach((part, idx) => {
            accum += (accum ? '/' : '') + part;
            const currentAccum = accum;

            const sep = document.createElement('span');
            sep.className = 'crumb-sep';
            sep.textContent = '/';
            container.appendChild(sep);

            const crumb = document.createElement('span');
            crumb.className = 'crumb' + (idx === parts.length - 1 ? ' active' : '');
            crumb.textContent = part;
            if (idx !== parts.length - 1) {
                crumb.onclick = () => loadFiles(currentAccum);
            }
            container.appendChild(crumb);
        });
    }

    function filterAndRenderFiles() {
        const query = document.getElementById('searchInput').value.toLowerCase().trim();
        state.filteredFiles = state.files.filter(f => {
            return f.name.toLowerCase().includes(query);
        });

        document.getElementById('fileCountBadge').textContent = `${state.filteredFiles.length} items`;
        renderFilesList();
    }

    document.getElementById('searchInput').addEventListener('input', filterAndRenderFiles);

    // Category Tabs Switching
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            const cat = btn.getAttribute('data-cat');
            state.currentCategory = cat;

            if (cat === 'all') loadFiles('');
            else if (cat === 'uploads') loadFiles('media/uploads');
            else if (cat === 'youtube') loadFiles('media/yt_library');
            else if (cat === 'trash') loadFiles('.pycloud_trash');
        });
    });

    function getFileIcon(type) {
        switch (type) {
            case 'folder': return '📁';
            case 'video': return '🎬';
            case 'audio': return '🎵';
            case 'image': return '🖼️';
            case 'document': return '📄';
            default: return '📦';
        }
    }

    function renderFilesList() {
        const container = document.getElementById('filesList');
        container.textContent = ''; // XSS Safe wipe

        if (state.filteredFiles.length === 0) {
            const empty = document.createElement('div');
            empty.className = 'empty-state';
            empty.textContent = 'No files found in this location';
            container.appendChild(empty);
            return;
        }

        state.filteredFiles.forEach(file => {
            const row = document.createElement('div');
            row.className = 'file-row';

            // Column 1: Name and icon
            const colName = document.createElement('div');
            colName.className = 'col-name';

            const icon = document.createElement('span');
            icon.className = 'file-icon';
            icon.textContent = getFileIcon(file.type);

            const nameLink = document.createElement('span');
            nameLink.className = 'file-name';
            nameLink.textContent = file.name; // textContent protects against Stored XSS

            if (file.is_dir) {
                nameLink.onclick = () => loadFiles(file.path);
            } else if (file.type === 'video' || file.type === 'audio' || file.type === 'image') {
                nameLink.onclick = () => openCinema(file);
            } else {
                nameLink.onclick = () => downloadFile(file.path, file.name);
            }

            colName.appendChild(icon);
            colName.appendChild(nameLink);

            // Column 2: Size
            const colSize = document.createElement('div');
            colSize.className = 'col-size';
            colSize.textContent = file.is_dir ? '—' : file.human_size;

            // Column 3: Modified Date
            const colDate = document.createElement('div');
            colDate.className = 'col-date';
            if (file.mod_time > 0) {
                const d = new Date(file.mod_time * 1000);
                colDate.textContent = d.toLocaleDateString() + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            } else {
                colDate.textContent = '—';
            }

            // Column 4: Actions
            const colActions = document.createElement('div');
            colActions.className = 'col-actions';

            if (!file.is_dir) {
                const dlBtn = document.createElement('button');
                dlBtn.className = 'btn btn-icon';
                dlBtn.title = 'Download';
                dlBtn.textContent = '⬇️';
                dlBtn.onclick = () => downloadFile(file.path, file.name);
                colActions.appendChild(dlBtn);
            }

            const renBtn = document.createElement('button');
            renBtn.className = 'btn btn-icon';
            renBtn.title = 'Rename';
            renBtn.textContent = '✏️';
            renBtn.onclick = () => openRenameModal(file.path, file.name);
            colActions.appendChild(renBtn);

            const delBtn = document.createElement('button');
            delBtn.className = 'btn btn-icon';
            delBtn.title = 'Delete';
            delBtn.textContent = '🗑️';
            delBtn.onclick = () => deleteItem(file.path, file.name);
            colActions.appendChild(delBtn);

            row.appendChild(colName);
            row.appendChild(colSize);
            row.appendChild(colDate);
            row.appendChild(colActions);

            container.appendChild(row);
        });
    }

    function downloadFile(path, filename) {
        const link = document.createElement('a');
        link.href = `/api/download?path=${encodeURIComponent(path)}&dl=1`;
        link.download = filename;
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
    }

    // Cinema Mode Media Player
    async function openCinema(file) {
        const modal = document.getElementById('cinemaModal');
        const title = document.getElementById('cinemaTitle');
        const dlBtn = document.getElementById('cinemaDlBtn');
        const content = document.getElementById('cinemaContent');

        title.textContent = file.name;
        dlBtn.href = `/api/download?path=${encodeURIComponent(file.path)}&dl=1`;
        dlBtn.download = file.name;
        content.textContent = ''; // Wipe

        const streamURL = `/api/media/stream?path=${encodeURIComponent(file.path)}`;

        if (file.type === 'video') {
            const video = document.createElement('video');
            video.controls = true;
            video.autoplay = true;
            video.src = streamURL;

            // Check if subtitle track exists
            try {
                const infoRes = await apiFetch(`/api/media/info?path=${encodeURIComponent(file.path)}`);
                if (infoRes.ok) {
                    const infoData = await infoRes.json();
                    if (infoData.has_subtitles) {
                        const track = document.createElement('track');
                        track.kind = 'subtitles';
                        track.src = infoData.subtitle_url;
                        track.srclang = 'en';
                        track.label = 'English';
                        track.default = true;
                        video.appendChild(track);
                    }
                }
            } catch (err) {}

            content.appendChild(video);
        } else if (file.type === 'audio') {
            const audio = document.createElement('audio');
            audio.controls = true;
            audio.autoplay = true;
            audio.src = streamURL;
            content.appendChild(audio);
        } else if (file.type === 'image') {
            const img = document.createElement('img');
            img.src = streamURL;
            img.alt = file.name;
            content.appendChild(img);
        }

        showModal('cinemaModal');
    }

    // Rename Item
    function openRenameModal(path, currentName) {
        document.getElementById('renameTargetOldPath').value = path;
        document.getElementById('renameInput').value = currentName;
        document.getElementById('renameFeedback').textContent = '';
        showModal('renameModal');
    }

    document.getElementById('btnSubmitRename').addEventListener('click', async () => {
        const oldPath = document.getElementById('renameTargetOldPath').value;
        const newName = document.getElementById('renameInput').value.trim();
        const feedback = document.getElementById('renameFeedback');

        if (!newName) {
            feedback.textContent = 'Name cannot be empty';
            return;
        }

        try {
            const res = await apiFetch('/api/rename', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ old_path: oldPath, new_name: newName })
            });

            if (!res.ok) {
                const err = await res.json();
                feedback.textContent = err.error || 'Failed to rename';
                return;
            }

            closeModal('renameModal');
            showToast('Item renamed successfully', 'success');
            loadFiles(state.currentPath);
        } catch (err) {
            feedback.textContent = err.message || 'Rename failed';
        }
    });

    // Delete Item
    async function deleteItem(path, name) {
        const isTrash = state.currentCategory === 'trash';
        const msg = isTrash 
            ? `Permanently delete "${name}"? This cannot be undone.` 
            : `Move "${name}" to trash?`;

        if (!confirm(msg)) return;

        try {
            const res = await apiFetch('/api/delete', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: path, permanent: isTrash })
            });

            if (!res.ok) {
                const err = await res.json();
                showToast(err.error || 'Delete failed', 'error');
                return;
            }

            showToast(isTrash ? 'Item permanently deleted' : 'Item moved to trash', 'success');
            loadFiles(state.currentPath);
        } catch (err) {
            showToast('Delete error: ' + err.message, 'error');
        }
    }

    // New Folder
    document.getElementById('btnNewFolder').addEventListener('click', () => {
        document.getElementById('mkdirInput').value = '';
        document.getElementById('mkdirFeedback').textContent = '';
        showModal('mkdirModal');
    });

    document.getElementById('btnSubmitMkdir').addEventListener('click', async () => {
        const name = document.getElementById('mkdirInput').value.trim();
        const feedback = document.getElementById('mkdirFeedback');

        if (!name) {
            feedback.textContent = 'Please enter folder name';
            return;
        }

        try {
            const res = await apiFetch('/api/mkdir', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ parent_path: state.currentPath, dir_name: name })
            });

            if (!res.ok) {
                const err = await res.json();
                feedback.textContent = err.error || 'Failed to create folder';
                return;
            }

            closeModal('mkdirModal');
            showToast(`Folder "${name}" created`, 'success');
            loadFiles(state.currentPath);
        } catch (err) {
            feedback.textContent = err.message || 'Creation failed';
        }
    });

    // 6. File Uploads (Drag & Drop + Input)
    const dropZone = document.getElementById('dropZone');
    const fileInput = document.getElementById('fileInput');

    document.getElementById('browseBtn').addEventListener('click', () => {
        fileInput.click();
    });

    ['dragenter', 'dragover'].forEach(eventName => {
        dropZone.addEventListener(eventName, (e) => {
            e.preventDefault();
            e.stopPropagation();
            dropZone.classList.add('dragover');
        });
    });

    ['dragleave', 'drop'].forEach(eventName => {
        dropZone.addEventListener(eventName, (e) => {
            e.preventDefault();
            e.stopPropagation();
            dropZone.classList.remove('dragover');
        });
    });

    dropZone.addEventListener('drop', (e) => {
        const files = e.dataTransfer.files;
        if (files.length > 0) uploadFiles(files);
    });

    fileInput.addEventListener('change', () => {
        if (fileInput.files.length > 0) uploadFiles(fileInput.files);
    });

    function uploadFiles(files) {
        if (!files || files.length === 0) return;

        const progressContainer = document.getElementById('uploadProgressContainer');
        const progressBar = document.getElementById('uploadProgressBar');
        const filenameLabel = document.getElementById('uploadFilename');
        const percentageLabel = document.getElementById('uploadPercentage');
        const cancelBtn = document.getElementById('btnCancelUpload');

        progressContainer.style.display = 'flex';
        filenameLabel.textContent = files.length === 1 ? files[0].name : `${files.length} files`;
        progressBar.style.width = '0%';
        percentageLabel.textContent = '0%';

        const fd = new FormData();
        for (let i = 0; i < files.length; i++) {
            fd.append('file', files[i]);
        }
        fd.append('path', state.currentPath || 'media/uploads');

        const xhr = new XMLHttpRequest();
        state.activeUploadXHR = xhr;

        xhr.upload.onprogress = (e) => {
            if (e.lengthComputable) {
                const pct = Math.round((e.loaded / e.total) * 100);
                progressBar.style.width = pct + '%';
                percentageLabel.textContent = pct + '%';
            }
        };

        xhr.onload = () => {
            state.activeUploadXHR = null;
            progressContainer.style.display = 'none';
            fileInput.value = '';

            if (xhr.status >= 200 && xhr.status < 300) {
                showToast('Upload completed successfully!', 'success');
                loadFiles(state.currentPath);
            } else {
                let err = 'Upload failed';
                try {
                    const parsed = JSON.parse(xhr.responseText);
                    err = parsed.error || err;
                } catch (e) {}
                showToast(err, 'error');
            }
        };

        xhr.onerror = () => {
            state.activeUploadXHR = null;
            progressContainer.style.display = 'none';
            showToast('Network error during file upload', 'error');
        };

        xhr.onabort = () => {
            state.activeUploadXHR = null;
            progressContainer.style.display = 'none';
            fileInput.value = '';
            showToast('Upload cancelled by user', 'info');
        };

        cancelBtn.onclick = () => {
            if (state.activeUploadXHR) state.activeUploadXHR.abort();
        };

        xhr.open('POST', '/api/upload', true);
        const csrf = getCSRFToken();
        if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf);
        xhr.send(fd);
    }

    // App Initialization
    async function initApp() {
        const canProceed = await checkServerStatus();
        if (!canProceed) return;

        loadFiles('');
        updateStats();
        updateTasks();

        if (!state.pollIntervalStats) {
            state.pollIntervalStats = setInterval(updateStats, 3000);
        }
        if (!state.pollIntervalTasks) {
            state.pollIntervalTasks = setInterval(updateTasks, 2000);
        }
    }

    // Start on DOM ready
    window.addEventListener('DOMContentLoaded', initApp);
})();
