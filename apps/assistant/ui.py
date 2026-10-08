"""Milestone 11: a lightweight chat front-end (no build step, no framework).

Served by the assistant itself, so the UI is same-origin with the JSON API.

UX: your message and a "thinking" bubble appear IMMEDIATELY on submit (optimistic),
not when the reply arrives; the reply then fills the placeholder. Includes a
resource-links bar, a live elapsed timer, per-reply metrics, toasts, and a Kokoro
speak control that swaps in a native <audio controls> (pause/seek).

The reply text is escaped server-side; the user's own text is inserted with
textContent so it is never parsed as HTML.
"""

import html

_PAGE = """<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>Cluster Assistant</title>
  <style>
    :root { color-scheme: dark; }
    * { box-sizing: border-box; }
    body { margin: 0; height: 100vh; display: flex; flex-direction: column;
           background: #0d1117; color: #e6edf3;
           font: 15px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
    header { padding: 10px 16px; border-bottom: 1px solid #21262d;
             display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
    header h1 { font-size: 15px; margin: 0; font-weight: 600; }
    header .muted { color: #7d8590; font-size: 12px; }
    .links { margin-left: auto; display: flex; gap: 8px; flex-wrap: wrap; }
    .links a { color: #58a6ff; text-decoration: none; font-size: 12px;
               border: 1px solid #30363d; border-radius: 6px; padding: 3px 8px; }
    .links a:hover { background: #161b22; }
    #thread { flex: 1; overflow-y: auto; padding: 16px; display: flex;
              flex-direction: column; gap: 10px; }
    .msg { max-width: 80ch; padding: 8px 12px; border-radius: 10px;
           white-space: pre-wrap; word-wrap: break-word; }
    .user { align-self: flex-end; background: #1f6feb; color: #fff; }
    .bot { align-self: flex-start; background: #161b22; border: 1px solid #21262d; }
    .bot.err { border-color: #f85149; color: #ffb4ad; }
    .bot.pending { color: #7d8590; }
    .meta { margin-top: 6px; font-size: 11px; color: #7d8590; }
    .tag { display: inline-block; margin-right: 6px; padding: 1px 6px;
           border-radius: 6px; background: #21262d; }
    .speak { margin-left: 8px; padding: 0 7px; font-size: 13px; line-height: 1.6;
             border: 1px solid #30363d; background: #0d1117; color: #e6edf3;
             border-radius: 6px; cursor: pointer; }
    .speak:disabled { opacity: .5; cursor: default; }
    audio { display: block; margin-top: 6px; width: 100%; height: 32px; }
    form { display: flex; gap: 8px; padding: 12px 16px; border-top: 1px solid #21262d; }
    select, input, button { font: inherit; border-radius: 8px; border: 1px solid #30363d;
           background: #0d1117; color: #e6edf3; padding: 8px 10px; }
    input[name=message] { flex: 1; }
    button.send { background: #238636; border-color: #2ea043; color: #fff; cursor: pointer; }
    button.send:disabled { opacity: .6; cursor: default; }
    #toast { position: fixed; right: 16px; bottom: 72px; display: flex;
             flex-direction: column; gap: 8px; z-index: 10; }
    .toast { background: #161b22; border: 1px solid #30363d; border-left: 3px solid #58a6ff;
             border-radius: 8px; padding: 8px 12px; font-size: 12px; max-width: 44ch;
             animation: fade .2s ease; }
    .toast.err { border-left-color: #f85149; }
    @keyframes fade { from { opacity: 0; transform: translateY(6px); } }
  </style>
</head>
<body>
  <header>
    <h1>Cluster Assistant</h1>
    <span class="muted">read-only &middot; local LLM</span>
    <nav class="links">__LINKS__</nav>
  </header>
  <main id="thread"></main>
  <form id="composer">
    <input type="hidden" name="session" id="session"/>
    <select name="mode" id="mode" title="investigate = multi-step tool loop; chat = one step">
      <option value="investigate">investigate</option>
      <option value="chat">chat</option>
    </select>
    <input name="message" id="message" placeholder="Ask about the cluster&hellip;"
           autocomplete="off" required autofocus/>
    <button class="send" type="submit">Send</button>
  </form>
  <div id="toast"></div>
  <script>
    var thread = document.getElementById("thread");
    (function () {
      var k = "assistant-session";
      var s = localStorage.getItem(k);
      if (!s) { s = (window.crypto && crypto.randomUUID) ? crypto.randomUUID() : String(Date.now()); localStorage.setItem(k, s); }
      document.getElementById("session").value = s;
    })();
    window.toast = function (msg, isErr) {
      var d = document.createElement("div");
      d.className = "toast" + (isErr ? " err" : "");
      d.textContent = msg;
      document.getElementById("toast").appendChild(d);
      setTimeout(function () { d.remove(); }, 6000);
    };
    // Optimistic send: show the user bubble + a thinking bubble right away.
    document.getElementById("composer").addEventListener("submit", function (e) {
      e.preventDefault();
      var input = document.getElementById("message");
      var text = input.value.trim();
      if (!text) return;
      var u = document.createElement("div");
      u.className = "msg user";
      u.textContent = text;
      thread.appendChild(u);
      var ph = document.createElement("div");
      ph.className = "msg bot pending";
      ph.innerHTML = "thinking&hellip; <b>0.0</b>s";
      thread.appendChild(ph);
      thread.scrollTop = thread.scrollHeight;
      input.value = "";
      var send = document.querySelector("button.send"); send.disabled = true;
      var t0 = performance.now();
      var tick = setInterval(function () {
        var b = ph.querySelector("b");
        if (b) b.textContent = ((performance.now() - t0) / 1000).toFixed(1);
      }, 100);
      var body = new URLSearchParams();
      body.set("message", text);
      body.set("mode", document.getElementById("mode").value);
      body.set("session", document.getElementById("session").value);
      fetch("/ui/message", { method: "POST",
                             headers: { "content-type": "application/x-www-form-urlencoded" },
                             body: body })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.text(); })
        .then(function (frag) { ph.outerHTML = frag; thread.scrollTop = thread.scrollHeight; })
        .catch(function (err) {
          ph.className = "msg bot err";
          ph.textContent = "Request failed: " + (err.message || err);
          window.toast("Request failed", true);
        })
        .finally(function () { clearInterval(tick); send.disabled = false; input.focus(); });
    });
    // Speak: fetch the WAV, then swap in a native audio element (pause/seek).
    document.addEventListener("click", function (e) {
      var b = e.target.closest("button.speak");
      if (!b) return;
      var label = b.textContent, text = b.getAttribute("data-text") || "";
      b.disabled = true; b.textContent = "\\u2026";
      var t0 = performance.now();
      fetch("/speak", { method: "POST", headers: { "content-type": "application/json" },
                        body: JSON.stringify({ message: text }) })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.blob(); })
        .then(function (blob) {
          var audio = document.createElement("audio");
          audio.controls = true; audio.autoplay = true; audio.src = URL.createObjectURL(blob);
          b.replaceWith(audio);
          window.toast("TTS ready in " + ((performance.now() - t0) / 1000).toFixed(2) + "s \\u2014 pause/seek enabled");
        })
        .catch(function (err) { b.disabled = false; b.textContent = label; window.toast("TTS failed: " + err.message, true); });
    });
  </script>
</body>
</html>
"""


def index(links: list | None = None) -> str:
    """Render the page, with an optional row of resource links."""
    items = []
    for link in links or []:
        if isinstance(link, dict) and link.get("url"):
            label = html.escape(str(link.get("label", "link")))
            url = html.escape(str(link["url"]), quote=True)
            items.append(f'<a href="{url}" target="_blank" rel="noopener">{label}</a>')
    return _PAGE.replace("__LINKS__", "".join(items))


def _fmt_metrics(metrics: dict) -> str:
    bits = []
    if metrics.get("elapsed_ms") is not None:
        bits.append(f'{metrics["elapsed_ms"] / 1000:.2f}s')
    if metrics.get("ttft_ms") is not None:
        bits.append(f'ttft {metrics["ttft_ms"] / 1000:.2f}s')
    if metrics.get("llm_ms") is not None:
        bits.append(f'llm {metrics["llm_ms"] / 1000:.2f}s')
    if metrics.get("tool_ms") is not None:
        bits.append(f'tools {metrics["tool_ms"] / 1000:.2f}s')
    if metrics.get("steps") is not None:
        bits.append(f'{metrics["steps"]} step(s)')
    return " &middot; ".join(bits)


def assistant_bubble(reply: str, tool: str | None = None,
                     metrics: dict | None = None, error: bool = False) -> str:
    """Render ONLY the assistant bubble (the user bubble is drawn client-side)."""
    cls = "bot err" if error else "bot"
    tags = [f'<span class="tag">{html.escape(t)}</span>'
            for t in str(tool or "").split(",") if t]
    if metrics:
        text = _fmt_metrics(metrics)
        if text:
            tags.append(f'<span class="tag">{text}</span>')
    meta = f'<div class="meta">{"".join(tags)}</div>' if tags else ""
    speak = "" if error else (
        f'<button class="speak" data-text="{html.escape(reply, quote=True)}"'
        ' title="Speak this reply">\U0001f50a</button>'
    )
    return f'<div class="msg {cls}">{html.escape(reply)}{speak}{meta}</div>'


def user_bubble(text: str) -> str:
    """Render a user bubble (server-side helper; the UI draws it client-side)."""
    return f'<div class="msg user">{html.escape(text)}</div>'
