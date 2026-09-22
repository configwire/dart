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
  var LS_PROJECT = "cn_admin_project";
  var LS_ENV = "cn_admin_env";

  var state = {
    token: null, // memory-first copy
    projects: [],
    envs: [],
    projectId: null,
    envId: null,
    envSlug: "dev",
    flags: [],
    groups: {}, // group id -> name
    releases: [],
    rules: [],
    experiments: [],
    keys: [],
  };

  function $(id) { return document.getElementById(id); }

  function authHeaders() {
    // BARE token. Do NOT prefix with "TOKEN " (PocketBase data API 403s).
    return state.token ? { Authorization: state.token } : {};
  }

  function toast(msg) {
    var el = $("toast");
    if (!el) return;
    el.textContent = msg;
    el.hidden = !msg;
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

  function selectedProject() { return state.projectId; }

  function selectedEnv() {
    for (var i = 0; i < state.envs.length; i++) {
      if (state.envs[i].id === state.envId) return state.envs[i];
    }
    return null;
  }

  function envSlug() {
    var e = selectedEnv();
    return (e && e.slug) || state.envSlug || "dev";
  }

  function loadPersistedScope() {
    try {
      state.projectId = localStorage.getItem(LS_PROJECT);
      state.envId = localStorage.getItem(LS_ENV);
    } catch (e) { /* private mode */ }
  }

  function persistScope() {
    try {
      if (state.projectId) localStorage.setItem(LS_PROJECT, state.projectId);
      else localStorage.removeItem(LS_PROJECT);
      if (state.envId) localStorage.setItem(LS_ENV, state.envId);
      else localStorage.removeItem(LS_ENV);
    } catch (e) { /* private mode */ }
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

  function serverMessage(data) {
    if (data == null) return "";
    if (typeof data === "string") return data;
    if (data.message) return String(data.message);
    try { return JSON.stringify(data); } catch (e) { return "request failed"; }
  }

  function renderScopeHint() {
    var p = $("project-select");
    var e = $("env-select");
    var pn = p && p.selectedOptions && p.selectedOptions[0] ? p.selectedOptions[0].textContent : "";
    var en = e && e.selectedOptions && e.selectedOptions[0] ? e.selectedOptions[0].textContent : "";
    $("scope-hint").textContent = state.projectId
      ? ("project: " + pn + "  ·  env: " + (en || "(none)"))
      : "Pick a project to scope flags, releases, publish and stats.";
  }

  function renderProjectEnv() {
    var ps = $("project-select");
    var cur = state.projectId;
    ps.innerHTML = state.projects.length
      ? state.projects.map(function (p) {
          return '<option value="' + esc(p.id) + '">' + esc(p.name || p.id) + "</option>";
        }).join("")
      : '<option value="">(no projects)</option>';
    if (cur && state.projects.some(function (p) { return p.id === cur; })) ps.value = cur;
    else if (state.projects.length) { ps.value = state.projects[0].id; state.projectId = state.projects[0].id; }
    else state.projectId = null;

    var es = $("env-select");
    var ecur = state.envId;
    es.innerHTML = state.envs.length
      ? state.envs.map(function (e) {
          return '<option value="' + esc(e.id) + '">' + esc(e.slug || e.id) + "</option>";
        }).join("")
      : '<option value="">(no envs)</option>';
    if (ecur && state.envs.some(function (e) { return e.id === ecur; })) es.value = ecur;
    else if (state.envs.length) {
      es.value = defaultEnvId();
      state.envId = es.value;
    } else state.envId = null;
    var sel = selectedEnv();
    if (sel && sel.slug) state.envSlug = sel.slug;
    renderScopeHint();
  }

  function defaultEnvId() {
    for (var i = 0; i < state.envs.length; i++) {
      if (state.envs[i].slug === "dev") return state.envs[i].id;
    }
    return state.envs.length ? state.envs[0].id : "";
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
          '<td><button type="button" data-stats-flag="' + esc(f.key) + '">stats</button> ' +
          '<button type="button" data-edit-flag="' + esc(f.id) + '">edit</button></td></tr>';
      });
    $("flag-tbody").innerHTML = rows.length
      ? rows.join("")
      : '<tr><td colspan="5">No flags for this project.</td></tr>';
    renderFlagSelects();
  }

  function renderFlagSelects() {
    var opts = state.flags.map(function (f) {
      return '<option value="' + esc(f.id) + '">' + esc(f.key) + "</option>";
    }).join("");
    var rs = $("rule-flag-select");
    var curR = rs.value;
    rs.innerHTML = opts || '<option value="">(no flags)</option>';
    if (curR) rs.value = curR;
    var xs = $("exp-flag-select");
    var curX = xs.value;
    xs.innerHTML = '<option value="">(none)</option>' + opts;
    if (curX !== undefined) xs.value = curX;
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
    if (!state.releases.length) { list.innerHTML = "<li>No releases for this env.</li>"; return; }
    list.innerHTML = state.releases.map(function (r) {
      return "<li>v" + esc(r.version) + " etag " + esc(r.etag) +
        (r.note ? " — " + esc(r.note) : "") +
        ' <button type="button" data-rollback-version="' + esc(r.version) + '">rollback to v' +
        esc(r.version) + "</button></li>";
    }).join("");
  }

  function renderRules() {
    var list = $("rule-list");
    if (!state.rules.length) { list.innerHTML = "<li>No rules for the selected flag.</li>"; return; }
    list.innerHTML = state.rules.map(function (r) {
      var flagKey = flagKeyById(r.flag) || r.flag || "";
      return "<li>p" + esc(r.priority) + " flag " + esc(flagKey) +
        " <code>" + esc(JSON.stringify(r.condition)) + "</code> → <code>" +
        esc(JSON.stringify(r.value)) + "</code></li>";
    }).join("");
  }

  function renderExperiments() {
    var list = $("experiment-list");
    if (!state.experiments.length) { list.innerHTML = "<li>No experiments.</li>"; return; }
    list.innerHTML = state.experiments.map(function (x) {
      return "<li>" + esc(x.name) + " [" + esc(x.status) + "] flag " +
        esc(flagKeyById(x.flag) || x.flag || "(none)") +
        " seed <code>" + esc(x.seed) + "</code></li>";
    }).join("");
  }

  function renderKeys() {
    var list = $("key-list");
    if (!state.keys.length) { list.innerHTML = "<li>No keys for this env.</li>"; return; }
    list.innerHTML = state.keys.map(function (k) {
      return "<li><code>" + esc(k.prefix) + "…</code>" +
        (k.revoked ? " revoked" : " active") +
        ' <button type="button" data-revoke-key="' + esc(k.id) + '"' +
        (k.revoked ? " disabled" : "") + ">revoke</button></li>";
    }).join("");
  }

  function flagKeyById(id) {
    for (var i = 0; i < state.flags.length; i++) {
      if (state.flags[i].id === id) return state.flags[i].key;
    }
    return "";
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
      if (!res.ok) {
        return res.json().then(function (data) {
          throw new Error("request failed (" + res.status + "): " + serverMessage(data));
        }, function () { throw new Error("request failed: " + path + " HTTP " + res.status); });
      }
      return res.json();
    });
  }

  function apiMut(method, path, body) {
    return fetch(path, {
      method: method,
      headers: Object.assign({ "Content-Type": "application/json" }, authHeaders()),
      body: body === undefined ? undefined : JSON.stringify(body),
    }).then(function (res) {
      return res.json().then(function (data) {
        return { status: res.status, data: data };
      }, function () {
        return { status: res.status, data: { message: "HTTP " + res.status } };
      }).then(function (out) {
        if (out.status === 401) logout();
        return out;
      });
    });
  }

  function loadProjects() {
    return api("/api/collections/projects/records?perPage=200").then(function (data) {
      state.projects = (data.items || []).slice().sort(function (a, b) {
        return (a.name || "") < (b.name || "") ? -1 : 1;
      });
      if (state.projectId && !state.projects.some(function (p) { return p.id === state.projectId; })) {
        state.projectId = null;
        state.envId = null;
      }
      if (!state.projectId && state.projects.length) state.projectId = state.projects[0].id;
      persistScope();
    });
  }

  function loadEnvs() {
    if (!state.projectId) { state.envs = []; state.envId = null; renderProjectEnv(); return Promise.resolve(); }
    var filter = "?perPage=200&filter=" + encodeURIComponent('(project="' + state.projectId + '")');
    return api("/api/collections/environments/records" + filter).then(function (data) {
      state.envs = (data.items || []).slice().sort(function (a, b) {
        return (a.slug || "") < (b.slug || "") ? -1 : 1;
      });
    }, function () {
      // Fallback: fetch all then filter client-side when the API filter fails.
      return api("/api/collections/environments/records?perPage=200").then(function (data) {
        state.envs = (data.items || []).filter(function (e) { return e.project === state.projectId; });
      });
    }).then(function () {
      if (state.envId && !state.envs.some(function (e) { return e.id === state.envId; })) state.envId = null;
      if (!state.envId && state.envs.length) state.envId = defaultEnvId();
      var sel = selectedEnv();
      if (sel && sel.slug) state.envSlug = sel.slug;
      persistScope();
      renderProjectEnv();
    });
  }

  function loadFlags() {
    var pid = state.projectId;
    var tryFiltered = pid
      ? api("/api/collections/flags/records?perPage=200&filter=" + encodeURIComponent('(project="' + pid + '")'))
      : api("/api/collections/flags/records?perPage=200");
    return tryFiltered.then(function (data) {
      var items = data.items || [];
      // Client-side fallback: keep only rows for the selected project.
      if (pid) items = items.filter(function (f) { return !f.project || f.project === pid; });
      state.flags = items.slice().sort(function (a, b) {
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
    // Fetch all then filter client-side by selected env relation; sort -version.
    return api("/api/collections/releases/records?perPage=200&sort=-version").then(function (data) {
      var items = data.items || [];
      if (state.envId) items = items.filter(function (r) { return !r.env || r.env === state.envId; });
      state.releases = items.slice().sort(function (a, b) { return b.version - a.version; });
      renderReleases();
      // Publish form auto-fills baseVersion from the latest version so the
      // first submit never goes stale (no 409 on first try by default).
      $("publish-base").value = latestVersion();
    });
  }

  function loadRules() {
    var flagId = $("rule-flag-select").value;
    var q = "/api/collections/rules/records?perPage=200&sort=priority";
    if (flagId) q += "&filter=" + encodeURIComponent('(flag="' + flagId + '")');
    return api(q).then(function (data) {
      var items = data.items || [];
      if (flagId) items = items.filter(function (r) { return !r.flag || r.flag === flagId; });
      state.rules = items.slice().sort(function (a, b) { return (a.priority || 0) - (b.priority || 0); });
      renderRules();
    }, function () {
      return api("/api/collections/rules/records?perPage=200&sort=priority").then(function (data) {
        var items = data.items || [];
        if (flagId) items = items.filter(function (r) { return r.flag === flagId; });
        state.rules = items;
        renderRules();
      });
    });
  }

  function loadExperiments() {
    return api("/api/collections/experiments/records?perPage=200").then(function (data) {
      var items = data.items || [];
      if (state.projectId) {
        var flagIds = {};
        state.flags.forEach(function (f) { flagIds[f.id] = true; });
        items = items.filter(function (x) { return !x.flag || flagIds[x.flag]; });
      }
      state.experiments = items;
      renderExperiments();
    });
  }

  function loadKeys() {
    return api("/api/collections/sdk_keys/records?perPage=200").then(function (data) {
      var items = data.items || [];
      if (state.envId) items = items.filter(function (k) { return !k.env || k.env === state.envId; });
      state.keys = items;
      renderKeys();
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

  function parseJSONInput(raw, label) {
    try {
      return { ok: true, value: raw === "" ? null : JSON.parse(raw) };
    } catch (e) {
      return { ok: false, error: label + " is not valid JSON" };
    }
  }

  function saveFlag(ev) {
    if (ev) ev.preventDefault();
    var id = $("flag-id").value;
    var parsed = parseJSONInput($("flag-default").value, "defaultValue");
    if (!parsed.ok) { $("flag-result").textContent = parsed.error; return Promise.resolve(); }
    var body = {
      key: $("flag-key").value.trim(),
      type: $("flag-type").value,
      defaultValue: parsed.value,
      project: state.projectId,
    };
    var group = $("flag-group").value.trim();
    if (group) body.group = group;
    var req = id
      ? apiMut("PATCH", "/api/collections/flags/records/" + encodeURIComponent(id), body)
      : apiMut("POST", "/api/collections/flags/records", body);
    return req.then(function (out) {
      $("flag-result").textContent = out.status === 200 || out.status === 201
        ? "flag saved: " + (out.data.key || out.data.id)
        : "flag save failed (" + out.status + "): " + serverMessage(out.data);
      if (out.status === 409) $("flag-result").textContent += " — refresh and retry";
      loadFlags().catch(function () {});
      return out;
    });
  }

  function createRule(ev) {
    if (ev) ev.preventDefault();
    var flagId = $("rule-flag-select").value;
    if (!flagId) { $("rule-result").textContent = "pick a flag first"; return Promise.resolve(); }
    var cond = parseJSONInput($("rule-condition").value, "condition");
    if (!cond.ok) { $("rule-result").textContent = cond.error; return Promise.resolve(); }
    var val = parseJSONInput($("rule-value").value, "value");
    if (!val.ok) { $("rule-result").textContent = val.error; return Promise.resolve(); }
    var body = {
      flag: flagId,
      priority: parseInt($("rule-priority").value, 10) || 0,
      condition: cond.value,
      value: val.value,
    };
    return apiMut("POST", "/api/collections/rules/records", body).then(function (out) {
      $("rule-result").textContent = out.status === 200 || out.status === 201
        ? "rule created"
        : "rule create failed (" + out.status + "): " + serverMessage(out.data);
      loadRules().catch(function () {});
      return out;
    });
  }

  function createExperiment(ev) {
    if (ev) ev.preventDefault();
    var variants;
    try {
      variants = JSON.parse($("exp-variants").value);
    } catch (e) { $("experiment-result").textContent = "variants is not valid JSON"; return Promise.resolve(); }
    var body = {
      name: $("exp-name").value.trim(),
      seed: $("exp-seed").value.trim(),
      variants: variants,
      status: $("exp-status").value,
    };
    var flagId = $("exp-flag-select").value;
    if (flagId) body.flag = flagId;
    return apiMut("POST", "/api/collections/experiments/records", body).then(function (out) {
      $("experiment-result").textContent = out.status === 200 || out.status === 201
        ? "experiment created: " + (out.data.name || out.data.id)
        : "experiment create failed (" + out.status + "): " + serverMessage(out.data);
      loadExperiments().catch(function () {});
      return out;
    });
  }

  function randomKey() {
    var bytes = new Uint8Array(24);
    if (window.crypto && window.crypto.getRandomValues) {
      window.crypto.getRandomValues(bytes);
    } else {
      for (var i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
    }
    var chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    var out = "";
    for (var j = 0; j < bytes.length; j++) out += chars[bytes[j] % chars.length];
    return "cn-" + out;
  }

  function sha256Hex(s) {
    if (window.crypto && window.crypto.subtle) {
      var bytes = new TextEncoder().encode(s);
      return window.crypto.subtle.digest("SHA-256", bytes).then(function (buf) {
        return Array.prototype.map.call(new Uint8Array(buf), function (b) {
          return ("0" + b.toString(16)).slice(-2);
        }).join("");
      });
    }
    return Promise.reject(new Error("WebCrypto unavailable — cannot hash key"));
  }

  function createKey(ev) {
    if (ev) ev.preventDefault();
    if (!state.envId) { $("key-result").textContent = "pick an environment first"; return Promise.resolve(); }
    var full = randomKey();
    var prefix = full.slice(0, 8);
    return sha256Hex(full).then(function (hash) {
      var body = {
        prefix: prefix,
        hash: hash,
        env: state.envId,
        rateLimit: parseInt($("key-ratelimit").value, 10) || 60,
      };
      return apiMut("POST", "/api/collections/sdk_keys/records", body).then(function (out) {
        if (out.status === 200 || out.status === 201) {
          $("key-result").textContent = "key created (prefix " + prefix + ")";
          $("key-once-value").textContent = full;
          $("key-once").hidden = false;
        } else {
          $("key-result").textContent = "key create failed (" + out.status + "): " + serverMessage(out.data);
        }
        loadKeys().catch(function () {});
        return out;
      });
    }, function (err) {
      $("key-result").textContent = err.message;
    });
  }

  function revokeKey(id) {
    return apiMut("PATCH", "/api/collections/sdk_keys/records/" + encodeURIComponent(id), { revoked: true })
      .then(function (out) {
        toast(out.status === 200 || out.status === 204
          ? "key revoked"
          : "revoke failed (" + out.status + "): " + serverMessage(out.data));
        loadKeys().catch(function () {});
        return out;
      });
  }

  function refreshAll() {
    loadPersistedScope();
    loadProjects().then(loadEnvs).then(function () {
      loadFlags().catch(function (e) { $("flag-tbody").innerHTML = "<tr><td colspan=5>" + esc(e.message) + "</td></tr>"; });
      loadReleases().catch(function (e) { $("release-list").innerHTML = "<li>" + esc(e.message) + "</li>"; });
      loadRules().catch(function (e) { $("rule-list").innerHTML = "<li>" + esc(e.message) + "</li>"; });
      loadExperiments().catch(function (e) { $("experiment-list").innerHTML = "<li>" + esc(e.message) + "</li>"; });
      loadKeys().catch(function (e) { $("key-list").innerHTML = "<li>" + esc(e.message) + "</li>"; });
    }).catch(function (e) { toast(e.message); });
  }

  // ---- wiring ----

  document.addEventListener("DOMContentLoaded", function () {
    // Restore session (localStorage copy; memory-first once loaded).
    try { state.token = localStorage.getItem(LS_KEY); } catch (e) { state.token = null; }
    loadPersistedScope();
    if (state.token) { setLoggedIn(true); refreshAll(); }

    $("login-form").addEventListener("submit", function (ev) {
      ev.preventDefault();
      login($("login-email").value, $("login-password").value).catch(function () { /* shown inline */ });
    });
    $("logout-btn").addEventListener("click", logout);
    $("refresh-scope").addEventListener("click", refreshAll);
    $("project-select").addEventListener("change", function () {
      state.projectId = $("project-select").value || null;
      state.envId = null;
      persistScope();
      loadEnvs().then(function () {
        loadFlags().catch(function () {});
        loadReleases().catch(function () {});
        loadExperiments().catch(function () {});
        loadKeys().catch(function () {});
      });
    });
    $("env-select").addEventListener("change", function () {
      state.envId = $("env-select").value || null;
      var sel = selectedEnv();
      if (sel && sel.slug) state.envSlug = sel.slug;
      persistScope();
      renderScopeHint();
      loadReleases().catch(function () {});
      loadKeys().catch(function () {});
    });
    $("refresh-flags").addEventListener("click", function () { loadFlags().catch(function (e) { toast(e.message); }); });
    $("refresh-releases").addEventListener("click", function () { loadReleases().catch(function (e) { toast(e.message); }); });
    $("refresh-rules").addEventListener("click", function () { loadRules().catch(function (e) { toast(e.message); }); });
    $("refresh-experiments").addEventListener("click", function () { loadExperiments().catch(function (e) { toast(e.message); }); });
    $("refresh-keys").addEventListener("click", function () { loadKeys().catch(function (e) { toast(e.message); }); });
    $("refresh-stats").addEventListener("click", function () {
      loadStats().catch(function (e) { $("stats-view").textContent = e.message; });
    });
    $("group-filter").addEventListener("change", renderFlags);
    $("rule-flag-select").addEventListener("change", function () { loadRules().catch(function () {}); });
    $("flag-form").addEventListener("submit", saveFlag);
    $("flag-reset").addEventListener("click", function () {
      $("flag-id").value = "";
      $("flag-form").reset();
    });
    $("rule-form").addEventListener("submit", createRule);
    $("experiment-form").addEventListener("submit", createExperiment);
    $("key-form").addEventListener("submit", createKey);
    $("key-copy").addEventListener("click", function () {
      var v = $("key-once-value").textContent;
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(v).then(function () { toast("copied"); }, function () { toast("copy failed"); });
      } else {
        var ta = document.createElement("textarea");
        ta.value = v;
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand("copy"); toast("copied"); }
        catch (e) { toast("copy failed"); }
        document.body.removeChild(ta);
      }
    });

    $("flag-tbody").addEventListener("click", function (ev) {
      var t = ev.target;
      var k = t && t.getAttribute && t.getAttribute("data-stats-flag");
      if (k) { $("stats-flag").value = k; loadStats().catch(function () {}); return; }
      var fid = t && t.getAttribute && t.getAttribute("data-edit-flag");
      if (fid) {
        for (var i = 0; i < state.flags.length; i++) {
          if (state.flags[i].id === fid) {
            var f = state.flags[i];
            $("flag-id").value = f.id;
            $("flag-key").value = f.key || "";
            $("flag-type").value = f.type || "bool";
            $("flag-group").value = f.group || "";
            $("flag-default").value = JSON.stringify(f.defaultValue === undefined ? null : f.defaultValue);
            $("flag-result").textContent = "editing " + (f.key || fid);
            break;
          }
        }
      }
    });

    $("release-list").addEventListener("click", function (ev) {
      var v = ev.target && ev.target.getAttribute && ev.target.getAttribute("data-rollback-version");
      if (!v) return;
      rollback(v, "rollback via admin UI").then(function (out) {
        $("publish-result").textContent = out.status === 200
          ? "rolled back: now v" + out.data.version
          : "rollback failed (" + out.status + "): " + serverMessage(out.data);
        loadReleases().catch(function () {});
      });
    });

    $("key-list").addEventListener("click", function (ev) {
      var id = ev.target && ev.target.getAttribute && ev.target.getAttribute("data-revoke-key");
      if (id) revokeKey(id);
    });

    $("publish-form").addEventListener("submit", function (ev) {
      ev.preventDefault();
      var base = parseInt($("publish-base").value, 10);
      publish($("publish-note").value, base).then(function (out) {
        if (out.status === 200) {
          $("publish-result").textContent = "published v" + out.data.version + " etag " + out.data.etag;
        } else if (out.status === 409) {
          $("publish-result").textContent = "stale baseVersion (409): currentVersion is " +
            out.data.currentVersion + " — refreshed latest, retry publish. " + serverMessage(out.data);
        } else {
          $("publish-result").textContent = "publish failed (" + out.status + "): " + serverMessage(out.data);
        }
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
    loadProjects: loadProjects, loadEnvs: loadEnvs,
    loadRules: loadRules, loadExperiments: loadExperiments, loadKeys: loadKeys,
    saveFlag: saveFlag, createRule: createRule, createExperiment: createExperiment,
    createKey: createKey, revokeKey: revokeKey,
    publish: publish, rollback: rollback,
  };
})();
