import http.server
import socketserver
import base64
import os
import cgi
import shutil
import urllib.parse
import subprocess
import json
import time
import threading
import queue
import uuid
import signal

# --- CONFIGURATION ---
PORT = 8080
USERNAME = "admin"
PASSWORD = "password"               # <-- Change this!
ROOT_DIR = "/home/user/folder"      # <-- Change this!
MEDIA_ROOT = os.path.join(ROOT_DIR, "media")
UPLOAD_DIR = os.path.join(MEDIA_ROOT, "uploads")
YT_DIR = os.path.join(MEDIA_ROOT, "yt_library")
YT_DLP_PATH = "./yt-dlp"

MAX_STREAM_SIZE = 400 * 1024 * 1024 
HIDDEN_FILES = ["nohup.out", "yt-dlp", "yt-dlp_linux", "secure_server.py", "__pycache__", "media"]
# ---------------------

def get_human_size(size_bytes):
    if size_bytes == 0: return "0B"
    size_name = ("B", "KB", "MB", "GB", "TB")
    i = 0
    while size_bytes >= 1024 and i < len(size_name)-1:
        size_bytes /= 1024.0
        i += 1
    return f"{size_bytes:.1f} {size_name[i]}"

class Task:
    def __init__(self, job_type, target, cmd=None):
        self.id = str(uuid.uuid4())[:8]
        self.type = job_type; self.target = target; self.cmd = cmd
        self.status = "waiting"; self.logs = []
        self.proc = None

    def log(self, message):
        self.logs.append(f"[{time.strftime('%H:%M:%S')}] {message}")
        if len(self.logs) > 50: self.logs.pop(0)

class TaskManager:
    def __init__(self):
        self.task_queue = queue.Queue(); self.tasks = []; self.lock = threading.Lock()
        threading.Thread(target=self._worker, daemon=True).start()

    def add_task(self, job_type, target, cmd):
        task = Task(job_type, target, cmd)
        with self.lock: self.tasks.insert(0, task)
        self.task_queue.put(task)
        return task.id

    def cancel_task(self, task_id):
        with self.lock:
            for t in self.tasks:
                if t.id == task_id:
                    if t.status == "running" and t.proc:
                        try: os.killpg(os.getpgid(t.proc.pid), signal.SIGTERM)
                        except: pass
                        t.log("Cancelled by user."); t.status = "cancelled"
                    elif t.status == "waiting": t.status = "cancelled"

    def retry_task(self, task_id):
        old = next((t for t in self.tasks if t.id == task_id), None)
        if old: self.add_task(old.type, old.target, old.cmd)

    def get_tasks_json(self):
        with self.lock: return [{"id":t.id,"type":t.type,"target":t.target,"status":t.status,"logs":t.logs} for t in self.tasks]

    def _worker(self):
        while True:
            task = self.task_queue.get()
            if task.status == "cancelled": continue
            task.status = "running"; task.log(f"Starting {task.type}...")
            try:
                # ONLY EXTERNAL COMMANDS (YT-DLP) NOW
                task.proc = subprocess.Popen(task.cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, preexec_fn=os.setsid)
                for line in iter(task.proc.stdout.readline, ''):
                    if task.status == "cancelled": break
                    if line.strip(): task.log(line.strip())
                task.proc.stdout.close()
                if task.status != "cancelled": task.status = "completed" if task.proc.wait() == 0 else "failed"
            except Exception as e:
                task.log(f"Error: {e}"); task.status = "failed"

manager = TaskManager()

class AuthHandler(http.server.SimpleHTTPRequestHandler):
    def do_HEAD(self): self.send_response(200); self.send_header("Content-type", "text/html"); self.end_headers()
    def do_AUTHHEAD(self): self.send_response(401); self.send_header("WWW-Authenticate", 'Basic realm="Private Cloud"'); self.send_header("Content-type", "text/html"); self.end_headers()
    
    def check_auth(self):
        key = base64.b64encode(f"{USERNAME}:{PASSWORD}".encode()).decode()
        if self.headers.get("Authorization") != "Basic " + key:
            self.do_AUTHHEAD(); self.wfile.write(b"NOT AUTHENTICATED"); return False
        return True

    def get_system_stats(self):
        total, used, free = shutil.disk_usage(ROOT_DIR)
        try: l1, _, _ = os.getloadavg(); cpu = f"{l1:.2f}"
        except: cpu = "N/A"
        try:
            with open('/proc/meminfo') as f: m = {l.split()[0].strip(':'): int(l.split()[1]) for l in f}
            ram = f"{(1 - m['MemAvailable']/m['MemTotal'])*100:.1f}%"
        except: ram = "N/A"
        return {"disk_pct": f"{(used/total)*100:.1f}%", "free_gb": f"{free/(1024**3):.1f} GB", "cpu_load": cpu, "ram_usage": ram}

    def render_player(self, filename, file_type="video"):
        display_name = urllib.parse.unquote(filename).split('/')[-1]
        vtt = os.path.splitext(filename)[0] + ".vtt"
        track = f'<track src="{vtt}" kind="subtitles" srclang="en" label="English" default>' if os.path.exists(vtt.lstrip('/')) else ""
        
        media_tag = ""
        if file_type == "video":
            media_tag = f'<video controls autoplay><source src="{filename}">{track}</video>'
        elif file_type == "image":
            media_tag = f'<img src="{filename}">'
        elif file_type == "audio":
            media_tag = f'<audio controls autoplay><source src="{filename}"></audio>'

        html = f"""<html><head><title>{display_name}</title><style>
            body{{background:#0d0d0d;color:#fff;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;font-family:sans-serif;}}
            video,img{{max-width:90%;max-height:80vh;box-shadow:0 0 30px #000;}}
            a{{color:#ccc;text-decoration:none;border:1px solid #333;padding:10px 20px;margin-top:20px;border-radius:6px;background:#1a1a1a;}}
            a:hover{{background:#333;color:#fff;}}
        </style></head><body><h2>{display_name}</h2>{media_tag}<br>
        <div>
            <a href="/"> &larr; Back to Library</a> 
            <a href="{filename}" download="{display_name}">&darr; Download</a>
        </div></body></html>"""
        self.send_response(200); self.send_header("Content-type", "text/html; charset=utf-8"); self.end_headers(); self.wfile.write(html.encode())

    def do_GET(self):
        if self.path == '/logout':
            self.do_AUTHHEAD(); self.wfile.write(b"<h1>Logged Out</h1><p>Close this window.</p>"); return

        if not self.check_auth(): return
        
        if self.path == '/api/stats':
            self.send_response(200); self.send_header("Content-type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps(self.get_system_stats()).encode()); return
        
        if self.path == '/api/tasks':
            self.send_response(200); self.send_header("Content-type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps(manager.get_tasks_json()).encode()); return

        p = urllib.parse.urlparse(self.path)
        if p.path == '/watch':
            q = urllib.parse.parse_qs(p.query)
            if 'v' in q: self.render_player(q['v'][0], "video"); return
            if 'i' in q: self.render_player(q['i'][0], "image"); return
            if 'a' in q: self.render_player(q['a'][0], "audio"); return

        if self.path == '/':
            self.send_response(200); self.send_header("Content-type", "text/html; charset=utf-8"); self.end_headers()
            html = """
            <!DOCTYPE html>
            <html><head><meta charset="utf-8"><title>Private Cloud v8.6</title>
            <style>
                body { background: #121212; color: #e0e0e0; font-family: 'Segoe UI', sans-serif; max_width: 1000px; margin: 0 auto; padding: 20px; }
                h1 { font-weight: 300; } h2 { border-bottom: 1px solid #333; padding-bottom: 10px; margin-top: 40px; color: #64b5f6; }
                .top-bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
                .stats-row { display: flex; gap: 15px; margin-bottom: 20px; }
                .stat-card { background: #1e1e1e; padding: 15px; border-radius: 8px; flex: 1; text-align: center; border: 1px solid #333; }
                .stat-val { font-size: 1.4em; font-weight: bold; color: #fff; }
                
                .upload-box { background: #1e1e1e; padding: 20px; border-radius: 8px; border: 1px dashed #444; text-align: center; margin-bottom: 20px; }
                #searchInput { width: 100%; padding: 12px; background: #1e1e1e; border: none; color: #fff; border-radius: 8px; margin-bottom: 20px; }
                
                .file-item { display: flex; justify-content: space-between; padding: 10px 15px; background: #181818; border-bottom: 1px solid #2a2a2a; align-items: center; }
                .file-item:hover { background: #252525; }
                a.file-link { color: #ccc; text-decoration: none; cursor: pointer; }
                a.file-link:hover { color: #fff; text-decoration: underline; }
                .tag { font-size: 0.7em; background: #333; padding: 2px 6px; border-radius: 4px; margin-left: 10px; color: #888; }
                .size-label { font-size: 0.8em; color: #666; margin-left: 10px; margin-right: 10px; }
                
                .btn { padding: 5px 10px; border: none; border-radius: 4px; cursor: pointer; font-weight: bold; font-size: 0.8em; margin-left: 5px; }
                .btn-del { background: #cf6679; color: #000; }
                .btn-ren { background: #2196F3; color: #fff; }
                .btn-logout { background: #333; color: #888; text-decoration: none; padding: 8px 15px; border-radius: 4px; border: 1px solid #444; }
                
                #task-panel { background: #1a1a1a; border: 1px solid #333; border-radius: 8px; overflow: hidden; margin-top: 20px; }
                .task-header { background: #252525; padding: 10px 15px; font-weight: bold; border-bottom: 1px solid #333; display: flex; justify-content: space-between; }
                .task-list { max-height: 300px; overflow-y: auto; }
                .task-row { padding: 10px 15px; border-bottom: 1px solid #222; display: flex; justify-content: space-between; align-items: center; font-size: 0.9em; }
                .task-status { width: 80px; text-transform: uppercase; font-size: 0.8em; font-weight: bold; }
                .status-running { color: #2196F3; } .status-completed { color: #4CAF50; } .status-failed { color: #f44336; } .status-waiting { color: #FF9800; }
                .logs-box { display: none; background: #111; padding: 10px; font-family: monospace; font-size: 0.85em; color: #aaa; border-top: 1px solid #333; white-space: pre-wrap; }
                
                .fab { position: fixed; bottom: 30px; right: 30px; background: #B00020; color: white; width: 60px; height: 60px; border-radius: 50%; font-size: 24px; display: flex; align-items: center; justify-content: center; cursor: pointer; box-shadow: 0 5px 15px rgba(0,0,0,0.5); border:none;}
                .modal { display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%, -50%); background: #2c2c2c; padding: 20px; border-radius: 8px; width: 350px; box-shadow: 0 10px 40px #000; border: 1px solid #444; z-index: 1000; }
                .overlay { display: none; position: fixed; top:0; left:0; width:100%; height:100%; background: rgba(0,0,0,0.7); z-index: 900; }
                input[type=text] { width: 100%; padding: 10px; background: #181818; border: 1px solid #444; color: #fff; margin-bottom: 10px; box-sizing: border-box; border-radius: 4px;}
            </style>
            <script>
                var openLogIds = new Set();

                setInterval(() => {
                    fetch('/api/stats').then(r=>r.json()).then(d=>{
                        document.getElementById('cpu').innerText = d.cpu_load;
                        document.getElementById('ram').innerText = d.ram_usage;
                        document.getElementById('disk').innerText = d.free_gb;
                    });
                    fetch('/api/tasks').then(r=>r.json()).then(renderTasks);
                }, 2000);

                function renderTasks(tasks) {
                    const list = document.getElementById('task-list-body');
                    if (tasks.length === 0) { list.innerHTML = '<div style="padding:15px; color:#555; text-align:center;">No active tasks</div>'; return; }
                    
                    list.innerHTML = tasks.map(t => {
                        const isVisible = openLogIds.has(t.id) ? 'block' : 'none';
                        return `
                        <div class="task-container" onmouseleave="hideLogs('${t.id}')">
                            <div class="task-row">
                                <div style="flex:1;">
                                    <div style="font-weight:bold;">${t.type}: ${t.target}</div>
                                    <div style="font-size:0.8em; color:#666;">ID: ${t.id}</div>
                                </div>
                                <div class="task-status status-${t.status}">${t.status}</div>
                                <div>
                                    ${t.status === 'running' || t.status === 'waiting' ? `<button class="btn btn-del" onclick="taskAction('cancel', '${t.id}')">Cancel</button>` : ''}
                                    ${t.status === 'failed' || t.status === 'cancelled' ? `<button class="btn" style="background:#444;color:#fff" onclick="taskAction('retry', '${t.id}')">Retry</button>` : ''}
                                    <button class="btn" style="background:#333;color:#ccc" onclick="toggleLogs('${t.id}')">Logs</button>
                                </div>
                            </div>
                            <div id="logs-${t.id}" class="logs-box" style="display:${isVisible}">${t.logs.join('\\n')}</div>
                        </div>`;
                    }).join('');
                }

                function taskAction(action, id) {
                    fetch('/', { method: 'POST', headers: {'Content-Type': 'application/x-www-form-urlencoded'}, body: `task_action=${action}&task_id=${id}` });
                }
                
                function toggleLogs(id) {
                    const el = document.getElementById('logs-'+id);
                    if (el.style.display === 'none') { el.style.display = 'block'; openLogIds.add(id); }
                    else { el.style.display = 'none'; openLogIds.delete(id); }
                }

                function hideLogs(id) {
                    const el = document.getElementById('logs-'+id);
                    if(el) { el.style.display = 'none'; openLogIds.delete(id); }
                }

                function renameFile(path, currentName) {
                    let newName = prompt("Rename to:", currentName);
                    if (newName && newName !== currentName) {
                        fetch('/', { method: 'POST', headers: {'Content-Type': 'application/x-www-form-urlencoded'}, body: `rename_path=`+encodeURIComponent(path)+`&new_name=`+encodeURIComponent(newName) })
                        .then(() => setTimeout(() => window.location.reload(), 500));
                    }
                }

                var currentUpload = null;
                function uploadFile(e) {
                    e.preventDefault();
                    document.getElementById('status').innerText = "Starting...";
                    document.getElementById('cancelBtn').style.display = 'inline-block';
                    var fd = new FormData(); fd.append('file', document.getElementById('fInput').files[0]);
                    currentUpload = new XMLHttpRequest();
                    currentUpload.open('POST', '/', true);
                    currentUpload.upload.onprogress = e => { 
                        if (e.lengthComputable) {
                            var pct = Math.round((e.loaded/e.total)*100);
                            document.getElementById('prog').style.width = pct + '%'; 
                            document.getElementById('status').innerText = pct + '% Uploaded';
                        }
                    };
                    currentUpload.onload = () => { setTimeout(() => window.location.reload(), 500); };
                    currentUpload.onabort = () => { 
                        document.getElementById('status').innerText = "Cancelled ❌";
                        document.getElementById('prog').style.width = '0%';
                        document.getElementById('cancelBtn').style.display = 'none';
                    };
                    currentUpload.send(fd);
                }
                function abortUpload() { if (currentUpload) { currentUpload.abort(); currentUpload = null; } }
                
                function showModal(id) { document.getElementById('overlay').style.display='block'; document.getElementById(id).style.display='block'; }
                function closeModal() { document.getElementById('overlay').style.display='none'; document.querySelectorAll('.modal').forEach(m=>m.style.display='none'); }
                function ytDl() {
                    const val = document.getElementById('ytUrl').value;
                    fetch('/', { method: 'POST', headers: {'Content-Type': 'application/x-www-form-urlencoded'}, body: `yt_val=`+encodeURIComponent(val) })
                    .then(() => { alert("Task added to queue!"); closeModal(); });
                }
                function dlFile(url, name) { showModal('dlModal'); const btn = document.getElementById('dlBtn'); btn.href = url; btn.setAttribute('download', name); }
            </script>
            </head><body>
            <div id="overlay" class="overlay" onclick="closeModal()"></div>
            
            <div class="top-bar">
                <h1>☁️ Cloud v8.6</h1>
                <a href="/logout" class="btn-logout">Logout 🔒</a>
            </div>

            <div class="stats-row">
                <div class="stat-card">CPU <div id="cpu" class="stat-val" style="color:#FF9800">...</div></div>
                <div class="stat-card">RAM <div id="ram" class="stat-val" style="color:#2196F3">...</div></div>
                <div class="stat-card">Free <div id="disk" class="stat-val" style="color:#4CAF50">...</div></div>
            </div>

            <div id="task-panel">
                <div class="task-header">
                    <span>⚡ Background Task Manager</span>
                    <span style="font-size:0.8em; font-weight:normal; color:#888;">Live Updates</span>
                </div>
                <div id="task-list-body" class="task-list">
                    <div style="padding:15px; color:#555; text-align:center;">No active tasks</div>
                </div>
            </div>

            <br>
            <input type="text" id="searchInput" placeholder="Filter files..." onkeyup="var v=this.value.toLowerCase();document.querySelectorAll('.file-item').forEach(e=>e.style.display=e.innerText.toLowerCase().includes(v)?'flex':'none')">
            
            <div class="upload-box">
                <form onsubmit="uploadFile(event)">
                    <input type="file" id="fInput" required>
                    <input type="submit" value="Upload 🚀" class="btn" style="background:#03DAC6;color:#000">
                    <button type="button" id="cancelBtn" class="btn btn-del" style="display:none;" onclick="abortUpload()">Cancel ❌</button>
                </form>
                <div style="background:#333;height:5px;margin-top:10px;border-radius:4px;overflow:hidden"><div id="prog" style="background:#03DAC6;width:0%;height:100%"></div></div>
                <div id="status" style="margin-top:5px;color:#03DAC6"></div>
            </div>

            """

            def list_dir(d, prefix, title):
                if not os.path.exists(d): return ""
                out = f"<h2>{title}</h2><div class='file-list'>"
                files = sorted(os.listdir(d), key=lambda x: os.path.getmtime(os.path.join(d,x)), reverse=True)
                for f in files:
                    if f.startswith('.') or f in HIDDEN_FILES: continue
                    path = os.path.join(d, f)
                    rel = f"{prefix}/{f}" if prefix else f
                    sz = os.path.getsize(path) if os.path.exists(path) else 0
                    sz_str = get_human_size(sz)
                    
                    is_dir = os.path.isdir(path)
                    
                    ext = f.lower().split('.')[-1]
                    link = rel; click = ""; tag = ""; 
                    
                    if is_dir:
                        link = rel
                        tag = '<span class="tag">DIR</span>'
                    else:
                        if ext in ['mp4','mkv','webm']:
                            if sz > MAX_STREAM_SIZE: link="#"; click=f"onclick=\"dlFile('{rel}','{f}')\""; tag='<span class="tag">400MB+</span>'
                            else: link=f"/watch?v={urllib.parse.quote(rel)}"
                        elif ext in ['jpg','png']: link=f"/watch?i={urllib.parse.quote(rel)}"
                    
                    out += f"""<div class="file-item">
                        <div style="flex:1; overflow:hidden; display:flex; align-items:center;">
                            <a href="{link}" class="file-link" {click}>{f} {tag}</a>
                            <span class="size-label">({sz_str})</span>
                        </div>
                        <div class="actions">
                            <button class="btn btn-ren" onclick="renameFile('{rel}', '{f}')">✏️</button>
                            <form method="POST" style="margin:0;display:inline"><input type="hidden" name="del_file" value="{rel}"><input type="submit" value="Del" class="btn btn-del" onclick="return confirm('Delete?')"></form>
                        </div>
                    </div>"""
                out += "</div>"
                return out

            html += list_dir(UPLOAD_DIR, "media/uploads", "📂 Manual Uploads")
            html += list_dir(YT_DIR, "media/yt_library", "📺 YouTube Library")
            html += list_dir(ROOT_DIR, "", "📦 Old Root Files")
            
            html += """
            <button class="fab" onclick="showModal('ytModal')">▶</button>
            <div id="ytModal" class="modal">
                <h3>Fetch Video</h3>
                <input type="text" id="ytUrl" placeholder="Paste YouTube URL...">
                <button class="btn" style="background:#03DAC6;width:100%;color:#000;padding:10px" onclick="ytDl()">Download to Server</button>
                <button class="btn" style="margin-top:10px;width:100%;background:#444;color:#fff" onclick="closeModal()">Close</button>
            </div>
            <div id="dlModal" class="modal">
                <h3>⚠ Large File</h3>
                <p>Use direct download for this file.</p>
                <a id="dlBtn" class="btn" style="background:#03DAC6;color:#000;display:block;text-align:center;text-decoration:none;padding:10px" download>Download ⬇</a>
                <button class="btn" style="margin-top:10px;width:100%;background:#444;color:#fff" onclick="closeModal()">Close</button>
            </div>
            </body></html>"""
            self.wfile.write(html.encode())
        else:
            super().do_GET()

    def do_POST(self):
        if not self.check_auth(): return
        ctype = self.headers.get('Content-Type')
        if ctype and 'multipart/form-data' in ctype:
            try:
                if not os.path.exists(UPLOAD_DIR): os.makedirs(UPLOAD_DIR)
                fs = cgi.FieldStorage(fp=self.rfile, headers=self.headers, environ={'REQUEST_METHOD':'POST'})
                if 'file' in fs and fs['file'].filename:
                    fn = os.path.basename(fs['file'].filename)
                    with open(os.path.join(UPLOAD_DIR, fn), 'wb') as f: shutil.copyfileobj(fs['file'].file, f)
                    self.send_response(200); self.end_headers(); return
            except: pass
        else:
            length = int(self.headers.get('content-length'))
            data = urllib.parse.parse_qs(self.rfile.read(length).decode('utf-8'))
            if 'yt_val' in data:
                val = data['yt_val'][0]
                if not os.path.exists(YT_DIR): os.makedirs(YT_DIR)
                cmd = [YT_DLP_PATH, "--restrict-filenames", "-o", f"{YT_DIR}/%(title)s.%(ext)s", val]
                manager.add_task("YouTube", val, cmd)
                self.send_response(200); self.end_headers(); return

            elif 'del_file' in data:
                path = os.path.abspath(os.path.join(ROOT_DIR, data['del_file'][0]))
                if path.startswith(ROOT_DIR):
                    try:
                        if os.path.isdir(path): shutil.rmtree(path)
                        else: os.remove(path)
                    except: pass
                self.send_response(303); self.send_header('Location', '/'); self.end_headers(); return

            elif 'rename_path' in data:
                old_rel = data['rename_path'][0]
                new_name = os.path.basename(data['new_name'][0])
                old_abs = os.path.abspath(os.path.join(ROOT_DIR, old_rel))
                new_abs = os.path.join(os.path.dirname(old_abs), new_name)
                if old_abs.startswith(ROOT_DIR) and new_abs.startswith(ROOT_DIR):
                    try: os.rename(old_abs, new_abs)
                    except: pass
                self.send_response(200); self.end_headers(); return

            elif 'task_action' in data:
                aid = data['task_id'][0]; act = data['task_action'][0]
                if act == 'cancel': manager.cancel_task(aid)
                if act == 'retry': manager.retry_task(aid)
                self.send_response(200); self.end_headers(); return
            
            self.send_response(200); self.end_headers()

if __name__ == "__main__":
    os.chdir(ROOT_DIR)
    for d in [UPLOAD_DIR, YT_DIR]:
        if not os.path.exists(d): os.makedirs(d)
    class ThreadingHTTPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
        allow_reuse_address = True; daemon_threads = True
    with ThreadingHTTPServer(("", PORT), AuthHandler) as httpd:
        print(f"Serving Cloud v8.6 on port {PORT}")
        httpd.serve_forever()