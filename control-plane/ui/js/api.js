export const $ = (s, el=document) => el.querySelector(s);

// api wraps fetch with JSON headers and throws on non-2xx with the server's
// error message. The session cookie is sent automatically (same-origin). The
// thrown Error carries the server's machine-readable "code" (e.g.
// "session_full") so callers can branch on it.
export const api = {
  async req(path, opts={}) {
    opts.headers = Object.assign({"Content-Type":"application/json"}, opts.headers||{});
    const r = await fetch(path, opts);
    const data = await r.json().catch(()=>({}));
    if (!r.ok) {
      const err = new Error(data.error || ("HTTP " + r.status));
      err.code = data.code;
      err.data = data;
      throw err;
    }
    return data;
  }
};

export function fmtUsd(v) { return "$" + (v||0).toFixed(4); }

// esc renders arbitrary text as safe plain HTML (no injection).
export function esc(s) {
  const d = document.createElement("div"); d.textContent = s; return d.innerHTML;
}

// messageMarkup renders one chat message: assistant messages carry the
// server-rendered markdown HTML; user messages are escaped plain text.
export function messageMarkup(m) {
  if (m.role === "assistant") {
    return `<div class="role">assistant</div><div class="bubble">${m.html || esc(m.text)}</div>`;
  }
  return `<div class="role">${esc(m.role)}</div><div class="bubble">${esc(m.text)}</div>`;
}

// fillMessages renders a list of messages into a container with a role label.
export function fillMessages(container, msgs) {
  container.innerHTML = "";
  if (!msgs.length) {
    container.innerHTML = `<div class="notice">No messages yet.</div>`;
    return;
  }
  for (const m of msgs) {
    const el = document.createElement("div");
    el.className = "msg " + m.role;
    el.innerHTML = messageMarkup(m);
    container.appendChild(el);
  }
}