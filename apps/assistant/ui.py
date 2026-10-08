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
import json
import re

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
    .bot.md { white-space: normal; }
    .md h1, .md h2, .md h3 { margin: 6px 0 4px; }
    .md h1 { font-size: 16px; } .md h2 { font-size: 15px; } .md h3 { font-size: 14px; }
    .md p { margin: 5px 0; }
    .md ul { margin: 5px 0; padding-left: 20px; }
    .md li { margin: 2px 0; }
    .md pre { background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
              padding: 8px; overflow-x: auto; }
    .md code { background: #21262d; border-radius: 4px; padding: 0 4px; }
    .md pre code { background: none; padding: 0; }
    .md a { color: #58a6ff; }
    .bot.pending { color: #7d8590; }
    .meta { margin-top: 6px; font-size: 11px; color: #7d8590; }
    .tag { display: inline-block; margin-right: 6px; padding: 1px 6px;
           border-radius: 6px; background: #21262d; }
    .evidence { margin-top: 6px; font-size: 12px; }
    .evidence > summary { cursor: pointer; color: #7d8590; }
    .evidence ul { margin: 4px 0; padding-left: 16px; }
    .evidence li { margin: 3px 0; }
    .evidence code { background: #21262d; border-radius: 4px; padding: 0 4px; }
    .evidence a { color: #58a6ff; text-decoration: none; }
    .copy { margin-left: 6px; font-size: 11px; border: 1px solid #30363d;
            background: #0d1117; color: #7d8590; border-radius: 5px;
            padding: 0 6px; cursor: pointer; }
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
      var audioUrl = b.getAttribute("data-audio");
      b.disabled = true; b.textContent = "\\u2026";
      function playBlob(blob) {
        var audio = document.createElement("audio");
        audio.controls = true; audio.autoplay = true; audio.src = URL.createObjectURL(blob);
        b.replaceWith(audio);
      }
      function speakOnDemand() {
        var t0 = performance.now();
        return fetch("/speak", { method: "POST", headers: { "content-type": "application/json" },
                                 body: JSON.stringify({ message: text }) })
          .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.blob(); })
          .then(function (blob) { playBlob(blob);
            window.toast("TTS ready in " + ((performance.now() - t0) / 1000).toFixed(2) + "s"); });
      }
      var p = audioUrl ? fetch(audioUrl).then(function (r) {
          if (r.status === 200) return r.blob();
          if (r.status === 202) throw { pending: true };
          throw new Error("HTTP " + r.status);
        }).then(function (blob) { playBlob(blob); window.toast("Played background TTS"); })
        : speakOnDemand();
      Promise.resolve(p).catch(function (err) {
        if (err && err.pending) { window.toast("TTS still generating\\u2026"); return speakOnDemand(); }
        window.toast("TTS failed: " + (err && err.message || err), true);
      }).finally(function () { b.disabled = false; b.textContent = label; });
    });
    // Evidence trail: copy-to-clipboard for a reproducible command/query.
    document.addEventListener("click", function (e) {
      var c = e.target.closest("button.copy");
      if (!c) return;
      var text = c.getAttribute("data-copy") || "";
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(
          function () { window.toast("Copied"); },
          function () { window.toast("Copy failed", true); });
      } else { window.toast("Copy failed", true); }
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


def _fmt_fact(fact: dict) -> str:
    """One evidence-trail row: tool, args, latency, reproducible command, links."""
    tool = html.escape(str(fact.get("tool", "?")))
    args = fact.get("args") or {}
    bits = []
    if args:
        bits.append(html.escape(json.dumps(args, sort_keys=True)))
    if fact.get("ms") is not None:
        bits.append(f'{fact["ms"]} ms')
    line = f"<code>{tool}</code>"
    if bits:
        line += " &middot; " + " &middot; ".join(bits)
    rep = fact.get("reproduce") or {}
    copyable = rep.get("cli") or rep.get("promql")
    if copyable:
        line += f" <code>{html.escape(str(copyable))}</code>"
        line += (f'<button class="copy" data-copy="{html.escape(str(copyable), quote=True)}"'
                 ' title="Copy">copy</button>')
    for name, url in (fact.get("links") or {}).items():
        line += (f' <a href="{html.escape(str(url), quote=True)}" target="_blank"'
                 f' rel="noopener">Open in {html.escape(str(name).title())}</a>')
    return f"<li>{line}</li>"


def _evidence_block(facts: list | None) -> str:
    facts = facts or []
    if not facts:
        return ""
    rows = "".join(_fmt_fact(f) for f in facts)
    return (f'<details class="evidence"><summary>Evidence ({len(facts)})</summary>'
            f"<ul>{rows}</ul></details>")


def assistant_bubble(reply: str, tool: str | None = None,
                     metrics: dict | None = None, error: bool = False,
                     speech: str | None = None, markdown: bool = False,
                     audio_id: str | None = None,
                     facts: list | None = None) -> str:
    """Render ONLY the assistant bubble (the user bubble is drawn client-side).

    `reply` is what is shown (Markdown-rendered when `markdown`); `speech` (if
    given) is what the 🔊 button speaks; `audio_id` points at the background-
    synthesized WAV so the button plays instantly instead of synthesizing;
    `facts` renders a collapsed, copyable evidence trail (never sent to the LLM).
    """
    cls = "bot err" if error else ("bot md" if markdown else "bot")
    tags = [f'<span class="tag">{html.escape(t)}</span>'
            for t in str(tool or "").split(",") if t]
    if metrics:
        text = _fmt_metrics(metrics)
        if text:
            tags.append(f'<span class="tag">{text}</span>')
    meta = f'<div class="meta">{"".join(tags)}</div>' if tags else ""
    evidence = "" if error else _evidence_block(facts)
    body = render_markdown(reply) if markdown else html.escape(reply)
    speak_text = speech or reply
    attr = (f' data-audio="/audio/{html.escape(str(audio_id), quote=True)}"'
            if audio_id else "")
    speak = "" if error else (
        f'<button class="speak" data-text="{html.escape(speak_text, quote=True)}"{attr}'
        ' title="Speak this reply">\U0001f50a</button>'
    )
    return f'<div class="msg {cls}">{body}{speak}{evidence}{meta}</div>'


def user_bubble(text: str) -> str:
    """Render a user bubble (server-side helper; the UI draws it client-side)."""
    return f'<div class="msg user">{html.escape(text)}</div>'


# --- screen_md: a minimal, safe Markdown subset -> HTML ----------------------

_HEADING = re.compile(r"^(#{1,4})\s+(.*)$")
_BULLET = re.compile(r"^\s*[-*]\s+(.*)$")
_LINK = re.compile(r"\[([^\]]+)\]\((https?://[^\s)]+)\)")
_BOLD = re.compile(r"\*\*([^*]+)\*\*")
_ITALIC = re.compile(r"(?<!\*)\*([^*]+)\*(?!\*)")
_INLINE_CODE = re.compile(r"`([^`]+)`")


def _inline(text: str) -> str:
    text = html.escape(text)                       # escape FIRST, then decorate
    text = _INLINE_CODE.sub(r"<code>\1</code>", text)
    text = _BOLD.sub(r"<strong>\1</strong>", text)
    text = _ITALIC.sub(r"<em>\1</em>", text)
    text = _LINK.sub(
        lambda m: f'<a href="{m.group(2)}" target="_blank" rel="noopener">{m.group(1)}</a>',
        text)
    return text


def render_markdown(md: str) -> str:
    """Render a safe subset of Markdown to HTML (no deps).

    The model's text is untrusted: everything is HTML-escaped first, then a small
    allowlist is applied — headings, bullet lists, fenced code, bold/italic,
    inline code, and http(s)-only links. No raw HTML is ever emitted.
    """
    out, in_code, in_list = [], False, False

    def close_list():
        nonlocal in_list
        if in_list:
            out.append("</ul>")
            in_list = False

    for raw in (md or "").splitlines():
        if raw.strip().startswith("```"):
            close_list()
            out.append("</code></pre>" if in_code else "<pre><code>")
            in_code = not in_code
            continue
        if in_code:
            out.append(html.escape(raw) + "\n")
            continue
        if not raw.strip():
            close_list()
            continue
        heading = _HEADING.match(raw)
        if heading:
            close_list()
            level = len(heading.group(1))
            out.append(f"<h{level}>{_inline(heading.group(2))}</h{level}>")
            continue
        bullet = _BULLET.match(raw)
        if bullet:
            if not in_list:
                out.append("<ul>")
                in_list = True
            out.append(f"<li>{_inline(bullet.group(1))}</li>")
            continue
        close_list()
        out.append(f"<p>{_inline(raw)}</p>")
    if in_code:
        out.append("</code></pre>")
    close_list()
    return "".join(out)
