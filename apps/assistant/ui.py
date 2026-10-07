"""Milestone 11: a tiny htmx chat front-end (no build step, no framework).

Served by the assistant itself, so the UI is same-origin with the JSON API. The
whole thing is one HTML page plus an HTML-fragment endpoint; htmx wires them.
Replies are escaped — the model's output is untrusted text.
"""

import html

INDEX = """<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>Cluster Assistant</title>
  <script src="https://unpkg.com/htmx.org@2.0.4"></script>
  <style>
    :root { color-scheme: dark; }
    * { box-sizing: border-box; }
    body { margin: 0; height: 100vh; display: flex; flex-direction: column;
           background: #0d1117; color: #e6edf3;
           font: 15px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
    header { padding: 12px 16px; border-bottom: 1px solid #21262d;
             display: flex; align-items: baseline; gap: 12px; }
    header h1 { font-size: 15px; margin: 0; font-weight: 600; }
    header .muted { color: #7d8590; font-size: 12px; }
    #thread { flex: 1; overflow-y: auto; padding: 16px; display: flex;
              flex-direction: column; gap: 10px; }
    .msg { max-width: 80ch; padding: 8px 12px; border-radius: 10px;
           white-space: pre-wrap; word-wrap: break-word; }
    .user { align-self: flex-end; background: #1f6feb; color: #fff; }
    .bot { align-self: flex-start; background: #161b22; border: 1px solid #21262d; }
    .bot.err { border-color: #f85149; color: #ffb4ad; }
    .tool { display: inline-block; margin-left: 8px; padding: 1px 6px;
            border-radius: 6px; background: #21262d; color: #7d8590; font-size: 11px; }
    form { display: flex; gap: 8px; padding: 12px 16px;
           border-top: 1px solid #21262d; }
    select, input, button { font: inherit; border-radius: 8px; border: 1px solid #30363d;
           background: #0d1117; color: #e6edf3; padding: 8px 10px; }
    input[name=message] { flex: 1; }
    button { background: #238636; border-color: #2ea043; color: #fff; cursor: pointer; }
    .htmx-indicator { opacity: 0; transition: opacity .15s; color: #7d8590; font-size: 12px; }
    .htmx-request .htmx-indicator, .htmx-request.htmx-indicator { opacity: 1; }
  </style>
</head>
<body>
  <header>
    <h1>Cluster Assistant</h1>
    <span class="muted">read-only &middot; local LLM</span>
    <span id="spin" class="htmx-indicator">thinking&hellip;</span>
  </header>
  <main id="thread"></main>
  <form hx-post="/ui/message" hx-target="#thread" hx-swap="beforeend"
        hx-indicator="#spin"
        hx-on::after-request="if(event.detail.successful){this.reset();const t=document.getElementById('thread');t.scrollTop=t.scrollHeight;}">
    <input type="hidden" name="session" id="session"/>
    <select name="mode" title="investigate runs a multi-step tool loop; chat answers in one step">
      <option value="investigate">investigate</option>
      <option value="chat">chat</option>
    </select>
    <input name="message" placeholder="Ask about the cluster&hellip;" autocomplete="off" required autofocus/>
    <button type="submit">Send</button>
  </form>
  <script>
    (function () {
      var k = "assistant-session";
      var s = localStorage.getItem(k);
      if (!s) {
        s = (window.crypto && crypto.randomUUID) ? crypto.randomUUID() : String(Date.now());
        localStorage.setItem(k, s);
      }
      document.getElementById("session").value = s;
      document.getElementById("thread").scrollIntoView();
    })();
  </script>
</body>
</html>
"""


def bubbles(user: str, reply: str, tool: str | None = None, error: bool = False) -> str:
    """Render one exchange (user + assistant) as an htmx-swappable fragment."""
    cls = "bot err" if error else "bot"
    badge = f'<span class="tool">{html.escape(tool)}</span>' if tool else ""
    return (
        f'<div class="msg user">{html.escape(user)}</div>'
        f'<div class="msg {cls}">{html.escape(reply)}{badge}</div>'
    )
