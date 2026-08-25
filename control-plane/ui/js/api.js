export const $ = (s, el=document) => el.querySelector(s);

// draft and expired park state across a lapsed session in sessionStorage, so
// a forced sign-in never destroys work: a message mid-send or text left in
// the compose box is restored on the next successful login.
const draft = {
  key: "rc_draft",
  save(v) { if (v) sessionStorage.setItem(this.key, v); },
  take() { const v = sessionStorage.getItem(this.key) || ""; sessionStorage.removeItem(this.key); return v; },
  clearIf(v) { if (v == null || sessionStorage.getItem(this.key) === v) sessionStorage.removeItem(this.key); },
};
const expired = {
  key: "rc_expired",
  set() { sessionStorage.setItem(this.key, "1"); },
  once() { const v = sessionStorage.getItem(this.key) === "1"; sessionStorage.removeItem(this.key); return v; },
};

// api wraps fetch with JSON headers and throws on non-2xx with the server's
// error message. The session cookie is sent automatically (same-origin). The
// thrown Error carries the server's machine-readable "code" (e.g.
// "session_full" or "auth_required") so callers can branch on it. A lapsed
// session (401 auth_required) is handled here, once, for every page.
export const api = {
  async req(path, opts={}) {
    opts.headers = Object.assign({"Content-Type":"application/json"}, opts.headers||{});
    const r = await fetch(path, opts);
    const data = await r.json().catch(()=>({}));
    if (!r.ok) {
      if (r.status === 401 && data.code === "auth_required") onAuthExpired();
      const err = new Error(data.error || ("HTTP " + r.status));
      err.code = data.code;
      err.data = data;
      throw err;
    }
    return data;
  }
};

// onAuthExpired reacts to a dead session wherever it surfaces: it parks the
// compose box, then reveals the login card in place (index page) or bounces
// to "/" (other pages, where the card lives). In both cases the throw still
// propagates so callers can stop rendering.
function onAuthExpired() {
  if (window.__rcAuthed) expired.set();
  const prompt = document.getElementById("prompt");
  if (prompt && prompt.value.trim()) draft.save(prompt.value);
  const login = document.getElementById("login");
  if (!login) {
    location.href = "/";
    return;
  }
  const main = document.getElementById("main");
  if (main) main.classList.remove("active");
  login.classList.add("active");
  const note = document.getElementById("loginNote");
  if (note && expired.once()) note.style.display = "block";
}

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