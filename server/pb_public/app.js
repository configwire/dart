/* ConfigNest admin shell logic (static only; all dynamic data via fetch).
 *
 * AUTH RULE (critical, from T11): the data-API superuser token is sent BARE
 * (`Authorization: <token>`), NEVER with a `TOKEN ` prefix (that 403s).
 * Token lives in memory first (`state.token`); a localStorage copy is kept
 * for convenience across reloads. XSS NOTE: any stored token is readable by
 * injected scripts — memory-first is the safer default; clear on logout and
 * prefer a fresh login on shared machines.
 */
(function () {
  "use strict";

  var LS_KEY = "cn_admin_token";

  var state = {
    token: null, // memory-first copy
    flags: [],
    groups: {}, // group id -> name
    releases: [],
  };

  function $(id) { return document.getElementById(id); }

  function authHeaders() {
    // BARE token. Do NOT prefix with "TOKEN " (PocketBase data API 403s).
    return state.token ? { Authorization: state.token } : {};
  }

  function showLoginError(msg) {
    var el = $("login-error");
    el.textContent = msg;
    el.hidden = !msg;
  }

  function setLoggedIn(on) {
    $("login-section").hidden = on;
    $("admin-section").hidden = !on;
    $("logout-btn").hidden = !on;
  }

  function envSlug() {
    return ($("env-input").value || "dev").trim() || "dev";
  }

  // ---- login ----

  function login(email, password) {
    showLoginError("");
    return fetch("/api/collections/_superusers/auth-with-password", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ identity: email, password: password }),
    }).then(function (res) {
      if (!res.ok) {
        state.token = null;
        try { localStorage.removeItem(LS_KEY); } catch (e) { /* private mode */ }
        throw new Error("login failed: HTTP " + res.status);
      }
      return res.json();
    }).then(function (data) {
      state.token = data.token; // bare token, memory-first
      try { localStorage.setItem(LS_KEY, data.token); } catch (e) { /* private mode */ }
      setLoggedIn(true);
      refreshAll();
      return data;
    }).catch(function (err) {
      showLoginError(err.message);
      throw err;
    });
  }

  function logout() {
    state.token = null;
    try { localStorage.removeItem(LS_KEY); } catch (e) { /* private mode */ }
    setLoggedIn(false);
  }

  // ---- render helpers ----

  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function renderFlags() {
    var groupFilter = $("group-filter").value;
    var rows = state.flags
      .filter(function (f) { return !groupFilter || (f.group || "") === groupFilter; })
      .map(function (f) {
        var gname = state.groups[f.group] || f.group || "";
        return "<tr><td>" + esc(f.key) + "</td><td>" + esc(f.type) + "</td>" +
          "<td>" + esc(gname) + "</td><td><code>" + esc(JSON.stringify(f.defaultValue)) +
          "</code></td>" +
          '<td><button type="button" data-stats-flag="' + esc(f.key) + '">stats</button></td></tr>';
      });
    $("flag-tbody").innerHTML = rows.length
      ? rows.join("")
      : '<tr><td colspan="5">No flags.</td></tr>';
  }

  function renderGroups() {
    var sel = $("group-filter");
    var cur = sel.value;
    sel.innerHTML = '<option value="">(all groups)</option>' +
      Object.keys(state.groups).map(function (id) {
        return '<option value="' + esc(id) + '">' + esc(state.groups[id]) + "</option>";
      }).join("");
    sel.value = cur;
  }

  function renderReleases() {
    var list = $("release-list");
    if (!state.releases.length) { list.innerHTML = "<li>No releases.</li>"; return; }
    list.innerHTML = state.releases.map(function (r) {
      return "<li>v" + esc(r.version) + " etag " + esc(r.etag) +
        (r.note ? " — " + esc(r.note) : "") +
        ' <button type="button" data-rollback-version="' + esc(r.version) + '">rollback to v' +
        esc(r.version) + "</button></li>";
    }).join("");
  }

  function latestVersion() {
    return state.releases.reduce(function (m, r) {
      return r.version > m ? r.version : m;
    }, 0);
  }

  function renderStats(data) {
    var split = Object.keys(data.perVariant || {}).map(function (v) {
      return esc(v === "" ? "(empty)" : v) + ": " + esc(data.perVariant[v]);
    }).join(", ") || "(no exposures)";
    $("stats-view").innerHTML =
      "<p>version: <strong>" + esc(data.version) + "</strong></p>" +
      "<p>fetches: <strong>" + esc(data.fetches) + "</strong></p>" +
      "<p>exposures: <strong>" + esc(data.exposures) + "</strong></p>" +
      "<p>split: " + split + "</p>";
  }

  // ---- data loaders (real endpoints) ----

  function api(path) {
    return fetch(path, { headers: authHeaders() }).then(function (res) {
      if (res.status === 401) { logout(); throw new Error("unauthorized (401) — please log in"); }
      if (!res.ok) throw new Error("request failed: " + path + " HTTP " + res.status);
      return res.json();
    });
  }

  function loadFlags() {
    return api("/api/collections/flags/records?perPage=200").then(function (data) {
      state.flags = (data.items || []).slice().sort(function (a, b) {
        return (a.key || "") < (b.key || "") ? -1 : 1;
      });
      return api("/api/collections/groups/records?perPage=200").then(function (g) {
        state.groups = {};
        (g.items || []).forEach(function (gr) { state.groups[gr.id] = gr.name || gr.id; });
        renderGroups();
        renderFlags();
      }, function () { renderFlags(); }); // flags still render if groups missing
    });
  }

  function loadReleases() {
    // Releases list via the data API (admin UI is superuser-gated).
    return api("/api/collections/releases/records?perPage=200&sort=-version").then(function (data) {
      state.releases = (data.items || []).slice().sort(function (a, b) { return b.version - a.version; });
      renderReleases();
      // Publish form auto-fills baseVersion from the latest version so the
      // first submit never goes stale (no 409 on first try by default).
      $("publish-base").value = latestVersion();
    });
  }

  function loadStats() {
    var flag = $("stats-flag").value.trim();
    var since = ($("stats-since").value.trim() || "7d");
    var url = "/api/v1/admin/env/" + encodeURIComponent(envSlug()) + "/stats?since=" +
      encodeURIComponent(since);
    if (flag) url += "&flag=" + encodeURIComponent(flag);
    return api(url).then(renderStats);
  }

  function publish(note, baseVersion) {
    return fetch("/api/v1/admin/env/" + encodeURIComponent(envSlug()) + "/publish", {
      method: "POST",
      headers: Object.assign({ "Content-Type": "application/json" }, authHeaders()),
      body: JSON.stringify({ note: note || "", baseVersion: baseVersion }),
    }).then(function (res) {
      return res.json().then(function (data) {
        return { status: res.status, data: data };
      });
    });
  }

  function rollback(version, note) {
    return fetch("/api/v1/admin/releases/" + encodeURIComponent(version) + "/rollback", {
      method: "POST",
      headers: Object.assign({ "Content-Type": "application/json" }, authHeaders()),
      body: JSON.stringify({ note: note || "" }),
    }).then(function (res) {
      return res.json().then(function (data) {
        return { status: res.status, data: data };
      });
    });
  }

  function refreshAll() {
    loadFlags().catch(function (e) { $("flag-tbody").innerHTML = "<tr><td colspan=5>" + esc(e.message) + "</td></tr>"; });
    loadReleases().catch(function (e) { $("release-list").innerHTML = "<li>" + esc(e.message) + "</li>"; });
  }

  // ---- wiring ----

  document.addEventListener("DOMContentLoaded", function () {
    // Restore session (localStorage copy; memory-first once loaded).
    try { state.token = localStorage.getItem(LS_KEY); } catch (e) { state.token = null; }
    if (state.token) { setLoggedIn(true); refreshAll(); }

    $("login-form").addEventListener("submit", function (ev) {
      ev.preventDefault();
      login($("login-email").value, $("login-password").value).catch(function () { /* shown inline */ });
    });
    $("logout-btn").addEventListener("click", logout);
    $("refresh-flags").addEventListener("click", function () { loadFlags().catch(function () {}); });
    $("refresh-releases").addEventListener("click", function () { loadReleases().catch(function () {}); });
    $("refresh-stats").addEventListener("click", function () {
      loadStats().catch(function (e) { $("stats-view").textContent = e.message; });
    });
    $("group-filter").addEventListener("change", renderFlags);

    $("flag-tbody").addEventListener("click", function (ev) {
      var k = ev.target && ev.target.getAttribute && ev.target.getAttribute("data-stats-flag");
      if (k) { $("stats-flag").value = k; loadStats().catch(function () {}); }
    });

    $("release-list").addEventListener("click", function (ev) {
      var v = ev.target && ev.target.getAttribute && ev.target.getAttribute("data-rollback-version");
      if (!v) return;
      rollback(v, "rollback via admin UI").then(function (out) {
        $("publish-result").textContent = out.status === 200
          ? "rolled back: now v" + out.data.version
          : "rollback failed (" + out.status + "): " + JSON.stringify(out.data);
        loadReleases().catch(function () {});
      });
    });

    $("publish-form").addEventListener("submit", function (ev) {
      ev.preventDefault();
      var base = parseInt($("publish-base").value, 10);
      publish($("publish-note").value, base).then(function (out) {
        $("publish-result").textContent = out.status === 200
          ? "published v" + out.data.version + " etag " + out.data.etag
          : "publish failed (" + out.status + "): " + JSON.stringify(out.data);
        loadReleases().catch(function () {});
      });
    });

    // HTMX: inject the BARE superuser token on every HTMX-driven request so
    // partials (if any) share the same auth as fetch calls.
    document.body.addEventListener("htmx:configRequest", function (ev) {
      if (state.token) ev.detail.headers["Authorization"] = state.token; // bare, no TOKEN prefix
    });
  });

  // Exposed for QA/contract checks (what the UI sends).
  window.cnAdmin = {
    state: state, login: login, logout: logout,
    loadFlags: loadFlags, loadReleases: loadReleases, loadStats: loadStats,
    publish: publish, rollback: rollback,
  };
})();
