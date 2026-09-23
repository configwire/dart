/* ConfigWire admin shell logic (static only; all dynamic data via fetch).
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

  var LS_KEY = "cw_admin_token";
  var LS_PROJECT = "cw_admin_project";
  var LS_ENV = "cw_admin_env";

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
    lastStats: null, // last successful stats payload (kept across errors)
    lastStatsText: "", // JSON text of lastStats for copy-JSON
    lastStatsError: "", // inline error line rendered under the numbers
  };

  function $(id) { return document.getElementById(id); }

  function on(id, ev, fn) {
    var el = $(id);
    if (el && el.addEventListener) el.addEventListener(ev, fn);
    return el;
  }

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
          '<button type="button" data-edit-flag="' + esc(f.id) + '">edit</button> ' +
          '<button type="button" data-delete-flag="' + esc(f.id) + '">delete</button></td></tr>';
      });
    $("flag-tbody").innerHTML = rows.length
      ? rows.join("")
      : '<tr><td colspan="5">No flags for this project.</td></tr>';
    renderFlagGroupSelect();
    renderFlagSelects();
    renderStatsFlagOptions();
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
    renderFlagGroupSelect();
  }

  function renderFlagGroupSelect() {
    var sel = $("flag-group");
    if (!sel) return;
    var cur = sel.value;
    sel.innerHTML = '<option value="">(no group)</option>' +
      Object.keys(state.groups).map(function (id) {
        return '<option value="' + esc(id) + '">' + esc(state.groups[id]) + "</option>";
      }).join("");
    if (cur && state.groups[cur]) sel.value = cur;
    else sel.value = "";
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
    // Percentages are display-only: counts are never recomputed from rates.
    // Charts are display-only too: div widths + conic-gradient stops derived
    // from perVariant/exposures shares, never recomputed counts.
    state.lastStats = data;
    try {
      state.lastStatsText = JSON.stringify(data, null, 2);
    } catch (e) { state.lastStatsText = String(data); }
    var exposures = Number(data.exposures) || 0;
    var perVariant = data.perVariant || {};
    var keys = Object.keys(perVariant);
    var palette = ["var(--accent)", "var(--ok)", "var(--warn)", "var(--danger)", "var(--muted)"];
    var entries = keys.map(function (v, i) {
      var label = v === "" ? "(empty)" : v;
      var count = Number(perVariant[v]) || 0;
      var pct = exposures > 0 ? (count / exposures * 100) : 0;
      return { label: label, pct: pct, color: palette[i % palette.length] };
    });
    var split = keys.map(function (v) {
      var label = v === "" ? "(empty)" : v;
      var count = perVariant[v];
      if (exposures > 0) {
        var pct = (Number(count) / exposures * 100).toFixed(1);
        return esc(label) + ": " + esc(count) + " (" + esc(pct) + "%)";
      }
      return esc(label) + ": " + esc(count);
    }).join(", ") || "(no exposures)";
    var chart = "";
    if (exposures > 0 && entries.length) {
      var bars = entries.map(function (e) {
        var pctText = e.pct.toFixed(1);
        return '<div class="stats-bar-row"><span class="stats-bar-label">' + esc(e.label) +
          '</span><span class="stats-bar-track" role="img" aria-label="' + esc(e.label) + " " + esc(pctText) + '%">' +
          '<span class="stats-bar-fill" style="width: ' + pctText + '%; background: ' + e.color + ';"></span></span>' +
          '<span class="stats-bar-pct">' + esc(pctText) + '%</span></div>';
      }).join("");
      var acc = 0;
      var stops = entries.map(function (e) {
        var s = acc;
        acc += e.pct;
        return e.color + " " + s.toFixed(1) + "% " + acc.toFixed(1) + "%";
      }).join(", ");
      var donutLabel = entries.map(function (e) { return e.label + " " + e.pct.toFixed(1) + "%"; }).join(", ");
      chart = '<div class="stats-chart"><div class="stats-bars">' + bars + "</div>" +
        '<div class="stats-donut" role="img" aria-label="Variant split: ' + esc(donutLabel) +
        '" style="background: conic-gradient(' + stops + ');"></div></div>';
    } else {
      chart = '<div class="stats-chart is-empty"><p class="muted stats-empty">No exposures yet — chart appears after the first exposure.</p></div>';
    }
    var echo = data.echo || {};
    var echoFlag = echo.flag !== undefined && echo.flag !== null && echo.flag !== "" ? echo.flag : "(all)";
    var echoSince = echo.since || echo.horizon || "";
    var echoHtml = "flag " + esc(echoFlag) + " · since " + esc(echoSince) +
      " · cutoff " + esc(echo.cutoff || "");
    // True empty (no exposures AND flag known): deliberate onboarding state —
    // muted tiles + one guidance line, quiet echo, secondary copy. Unknown-flag
    // zeros (flagFound:false) keep the legacy zero wall + prominent warning so
    // the two states never look alike; populated markup below is byte-identical.
    var isEmpty = exposures === 0 && data.flagFound !== false;
    var html;
    if (isEmpty) {
      html =
        '<div class="stats-tiles" role="group" aria-label="Current totals">' +
        '<div class="stats-tile"><span class="stats-tile-num">' + esc(data.version) + '</span><span class="stats-tile-label">version</span></div>' +
        '<div class="stats-tile"><span class="stats-tile-num">' + esc(data.fetches) + '</span><span class="stats-tile-label">fetches</span></div>' +
        '<div class="stats-tile"><span class="stats-tile-num">' + esc(data.exposures) + '</span><span class="stats-tile-label">exposures</span></div>' +
        "</div>" +
        '<p class="stats-guide">No stats yet — publish a release, fetch via SDK, then post an exposure event.</p>' +
        '<p class="muted stats-echo-quiet">' + echoHtml + "</p>";
    } else {
      html =
        '<p class="stats-count">version: <strong>' + esc(data.version) + "</strong></p>" +
        '<p class="stats-count">fetches: <strong>' + esc(data.fetches) + "</strong></p>" +
        '<p class="stats-count">exposures: <strong>' + esc(data.exposures) + "</strong></p>" +
        '<p class="stats-split">split: ' + split + "</p>" +
        chart +
        '<p class="muted">' + echoHtml + "</p>";
    }
    if (data.approximate) html += '<p class="muted">approximate</p>';
    if (data.flagFound === false) {
      html += "<p role=\"alert\">warning: unknown flag — showing zeros (flagFound:false).</p>";
    }
    html += '<p class="stats-copy-row' + (isEmpty ? " is-secondary" : "") + '"><button type="button" id="stats-copy" class="btn ghost">Copy JSON</button> ' +
      '<span id="stats-copy-status" class="muted" role="status"></span></p>';
    if (state.lastStatsError) html += "<p role=\"alert\">" + esc(state.lastStatsError) + "</p>";
    $("stats-view").innerHTML = html;
  }

  function copyStatsJson() {
    var status = $("stats-copy-status");
    function say(msg) {
      if (status) status.textContent = msg;
      else toast(msg);
    }
    var text = state.lastStatsText || "";
    if (!text) { say("nothing to copy yet"); return; }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () { say("copied"); },
        function () { say("copy failed — select and copy manually"); });
    } else {
      var ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand("copy"); say("copied"); }
      catch (e) { say("copy failed — select and copy manually"); }
      document.body.removeChild(ta);
    }
  }

  // Stats flag dropdown: populated from the already-loaded flags collection
  // (same perPage=200 superuser read as loadFlags — no new endpoint).
  function renderStatsFlagOptions() {
    var sel = $("stats-flag");
    if (!sel) return;
    var cur = sel.value;
    var keys = state.flags.map(function (f) { return f.key; })
      .filter(function (k) { return !!k; }).sort();
    var html = '<option value="">All flags</option>' + keys.map(function (k) {
      return '<option value="' + esc(k) + '">' + esc(k) + "</option>";
    }).join("");
    // Keep a stale/unknown selection visible so its warning round-trips.
    if (cur && keys.indexOf(cur) === -1) {
      html += '<option value="' + esc(cur) + '" selected>' + esc(cur) + "</option>";
    }
    sel.innerHTML = html;
    if (!cur) sel.value = "";
    else if (keys.indexOf(cur) !== -1) sel.value = cur;
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
      // Client-side filter: strict match only — legacy unscoped rows must
      // not leak across projects (groups carry a required project relation).
      if (pid) items = items.filter(function (f) { return f.project === pid; });
      state.flags = items.slice().sort(function (a, b) {
        return (a.key || "") < (b.key || "") ? -1 : 1;
      });
      var gq = "/api/collections/groups/records?perPage=200";
      if (pid) gq += "&filter=" + encodeURIComponent('(project="' + pid + '")');
      return api(gq).then(function (g) {
        state.groups = {};
        (g.items || []).forEach(function (gr) {
          if (pid && gr.project !== pid) return;
          state.groups[gr.id] = gr.name || gr.id;
        });
        renderGroups();
        renderFlags();
      }, function () { renderFlags(); }); // flags still render if groups missing
    });
  }

  function loadReleases() {
    // Fetch all then filter client-side by selected env relation; sort -version.
    return api("/api/collections/releases/records?perPage=200&sort=-version").then(function (data) {
      var items = data.items || [];
      if (state.envId) items = items.filter(function (r) { return r.env === state.envId; });
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
      if (flagId) items = items.filter(function (r) { return r.flag === flagId; });
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
        items = items.filter(function (x) { return flagIds[x.flag]; });
      }
      state.experiments = items;
      renderExperiments();
    });
  }

  function loadKeys() {
    return api("/api/collections/sdk_keys/records?perPage=200").then(function (data) {
      var items = data.items || [];
      if (state.envId) items = items.filter(function (k) { return k.env === state.envId; });
      state.keys = items;
      renderKeys();
    });
  }

  function loadStats() {
    var flagEl = $("stats-flag");
    var sinceEl = $("stats-since");
    var flag = flagEl && flagEl.value != null ? String(flagEl.value) : "";
    var sinceRaw = sinceEl && sinceEl.value != null ? String(sinceEl.value) : "";
    var since = sinceRaw !== "" ? sinceRaw : "7d";
    var url = "/api/v1/admin/env/" + encodeURIComponent(envSlug()) + "/stats?since=" +
      encodeURIComponent(since);
    if (flag) url += "&flag=" + encodeURIComponent(flag);
    if (state.projectId) url += "&project=" + encodeURIComponent(state.projectId);
    if (!state.lastStats) $("stats-view").textContent = "Loading…";
    state.lastStatsError = "";
    return api(url).then(function (data) {
      state.lastStatsError = "";
      renderStats(data);
      return data;
    }, function (err) {
      var msg = err && err.message ? String(err.message).split("\n")[0] : "stats load failed";
      state.lastStatsError = msg;
      if (state.lastStats) renderStats(state.lastStats);
      else $("stats-view").innerHTML = "<p>Loading… failed.</p><p role=\"alert\">" + esc(msg) + "</p>";
      throw err;
    });
  }

  function publish(note, baseVersion) {
    var url = "/api/v1/admin/env/" + encodeURIComponent(envSlug()) + "/publish";
    if (state.projectId) url += "?project=" + encodeURIComponent(state.projectId);
    return fetch(url, {
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
    var url = "/api/v1/admin/env/" + encodeURIComponent(envSlug()) +
      "/releases/" + encodeURIComponent(version) + "/rollback";
    if (state.projectId) url += "?project=" + encodeURIComponent(state.projectId);
    return fetch(url, {
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

  function jsonDetail(raw) {
    if (raw.trim() === "") return { ok: true, value: null };
    try {
      return { ok: true, value: JSON.parse(raw) };
    } catch (e) {
      return { ok: false, error: (e && e.message) ? e.message : "invalid JSON" };
    }
  }

  function setJsonHint(hintEl, inputEl, ok, msg) {
    if (!hintEl) return;
    hintEl.textContent = msg;
    hintEl.className = "json-hint" + (msg ? (ok ? " ok" : " err") : "");
    if (inputEl) {
      inputEl.classList.remove("valid", "invalid");
      if (msg) inputEl.classList.add(ok ? "valid" : "invalid");
    }
  }

  function updateJsonHint(inputId) {
    var input = $(inputId);
    if (!input) return false;
    var parsed = jsonDetail(input.value);
    setJsonHint(
      $(inputId + "-hint"),
      input,
      parsed.ok,
      parsed.ok ? (input.value.trim() === "" ? "" : "Valid JSON") : "Invalid JSON: " + parsed.error
    );
    return parsed.ok;
  }

  function updateFlagDefaultHint() {
    return updateJsonHint("flag-default");
  }

  function updateRuleConditionHint() {
    return updateJsonHint("rule-condition");
  }

  function updateRuleValueHint() {
    return updateJsonHint("rule-value");
  }

  function updateExpVariantsHint() {
    return updateJsonHint("exp-variants");
  }

  function updateAllJsonHints() {
    updateJsonHint("flag-default");
    updateJsonHint("rule-condition");
    updateJsonHint("rule-value");
    updateJsonHint("exp-variants");
  }

  var jsonEditorTarget = "flag-default";
  var jsonEditorLabels = {
    "flag-default": "defaultValue",
    "rule-condition": "condition",
    "rule-value": "value",
    "exp-variants": "variants",
  };

  function updateEditorStatus() {
    var ta = $("json-editor-text");
    var parsed = jsonDetail(ta.value);
    setJsonHint(
      $("json-editor-status"),
      ta,
      parsed.ok,
      parsed.ok ? "Valid JSON" : "Invalid JSON: " + parsed.error
    );
    var save = $("json-editor-save");
    if (save) save.disabled = !parsed.ok;
    return parsed.ok;
  }

  function openJsonEditorFor(targetId) {
    var input = $(targetId);
    if (!input) return;
    jsonEditorTarget = targetId;
    var label = jsonEditorLabels[targetId] || targetId;
    var title = $("json-editor-title");
    if (title) title.textContent = "Edit " + label + " (JSON)";
    var ta = $("json-editor-text");
    if (!ta) return;
    var parsed = jsonDetail(input.value);
    ta.value = parsed.ok && input.value.trim() !== ""
      ? JSON.stringify(parsed.value, null, 2)
      : input.value;
    updateEditorStatus();
    var dlg = $("json-editor-dialog");
    if (!dlg) return;
    if (dlg.showModal) {
      try {
        if (!dlg.open) dlg.showModal();
      } catch (e) { /* already open or unsupported — editor still usable inline */ }
    }
    try {
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    } catch (e2) { /* focus/selection is best-effort */ }
  }

  function openJsonEditor(evOrId) {
    if (typeof evOrId === "string" && $(evOrId)) return openJsonEditorFor(evOrId);
    if (evOrId) {
      var src = (evOrId.target && evOrId.target.closest &&
          evOrId.target.closest("[data-target]")) ||
        evOrId.currentTarget;
      if (src && src.getAttribute) {
        var t = src.getAttribute("data-target");
        if (t && $(t)) return openJsonEditorFor(t);
      }
    }
    return openJsonEditorFor(jsonEditorTarget);
  }

  function closeJsonEditor(save) {
    var dlg = $("json-editor-dialog");
    if (save) {
      if (!updateEditorStatus()) return;
      var input = $(jsonEditorTarget);
      if (input) {
        input.value = $("json-editor-text").value;
        updateJsonHint(jsonEditorTarget);
      }
    }
    if (dlg && dlg.open) dlg.close();
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
    var group = $("flag-group").value || "";
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

  function createProject(ev) {
    if (ev) ev.preventDefault();
    var name = $("project-name").value.trim();
    if (!name) { $("project-result").textContent = "project name is required"; return Promise.resolve(); }
    return apiMut("POST", "/api/collections/projects/records", { name: name }).then(function (out) {
      var ok = out.status === 200 || out.status === 201;
      $("project-result").textContent = ok
        ? "project created: " + (out.data.name || out.data.id)
        : "project create failed (" + out.status + "): " + serverMessage(out.data);
      if (ok) {
        toast("project created: " + (out.data.name || out.data.id));
        $("project-name").value = "";
        var newId = out.data.id;
        loadProjects().then(function () {
          if (newId) { state.projectId = newId; persistScope(); }
          return loadEnvs();
        }).then(function () {
          renderScopeHint();
          loadFlags().catch(function () {});
        }).catch(function () {});
      }
      return out;
    });
  }

  function createEnv(ev) {
    if (ev) ev.preventDefault();
    if (!state.projectId) { $("env-result").textContent = "pick a project first"; return Promise.resolve(); }
    var slug = $("env-slug").value.trim();
    if (!slug) { $("env-result").textContent = "env slug is required"; return Promise.resolve(); }
    return apiMut("POST", "/api/collections/environments/records", { project: state.projectId, slug: slug }).then(function (out) {
      var ok = out.status === 200 || out.status === 201;
      $("env-result").textContent = ok
        ? "env created: " + (out.data.slug || out.data.id)
        : "env create failed (" + out.status + "): " + serverMessage(out.data);
      if (ok) {
        toast("env created: " + (out.data.slug || out.data.id));
        $("env-slug").value = "";
        loadEnvs().catch(function () {});
      }
      return out;
    });
  }

  function createGroup(ev) {
    if (ev) ev.preventDefault();
    if (!state.projectId) { $("group-result").textContent = "select a project first"; return Promise.resolve(); }
    var name = $("group-name").value.trim();
    if (!name) { $("group-result").textContent = "group name is required"; return Promise.resolve(); }
    return apiMut("POST", "/api/collections/groups/records", { name: name, project: state.projectId }).then(function (out) {
      var ok = out.status === 200 || out.status === 201;
      $("group-result").textContent = ok
        ? "group created: " + (out.data.name || out.data.id)
        : "group create failed (" + out.status + "): " + serverMessage(out.data);
      if (ok) {
        toast("group created: " + (out.data.name || out.data.id));
        $("group-name").value = "";
        loadFlags().catch(function () {});
      }
      return out;
    });
  }

  function deleteFlag(id) {
    return apiMut("DELETE", "/api/collections/flags/records/" + encodeURIComponent(id)).then(function (out) {
      var ok = out.status === 200 || out.status === 201 || out.status === 204;
      toast(ok ? "flag deleted" : "flag delete failed (" + out.status + "): " + serverMessage(out.data));
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
    var parsed = parseJSONInput($("exp-variants").value, "variants");
    if (!parsed.ok) { $("experiment-result").textContent = parsed.error; return Promise.resolve(); }
    var variants = parsed.value;
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
    return "cw-" + out;
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
      loadStats().catch(function () { /* inline in stats card */ });
    }).catch(function (e) { toast(e.message); });
  }

  // ---- wiring ----

  document.addEventListener("DOMContentLoaded", function () {
    // Restore session (localStorage copy; memory-first once loaded).
    try { state.token = localStorage.getItem(LS_KEY); } catch (e) { state.token = null; }
    loadPersistedScope();
    if (state.token) { setLoggedIn(true); refreshAll(); }

    on("login-form", "submit", function (ev) {
      ev.preventDefault();
      login($("login-email").value, $("login-password").value).catch(function () { /* shown inline */ });
    });
    on("logout-btn", "click", logout);
    on("refresh-scope", "click", refreshAll);
    on("project-select", "change", function () {
      state.projectId = $("project-select").value || null;
      state.envId = null;
      persistScope();
      loadEnvs().then(function () {
        loadFlags().catch(function () {});
        loadReleases().catch(function () {});
        loadExperiments().catch(function () {});
        loadKeys().catch(function () {});
        loadStats().catch(function () {});
      });
    });
    on("env-select", "change", function () {
      state.envId = $("env-select").value || null;
      var sel = selectedEnv();
      if (sel && sel.slug) state.envSlug = sel.slug;
      persistScope();
      renderScopeHint();
      loadReleases().catch(function () {});
      loadKeys().catch(function () {});
      loadStats().catch(function () {});
    });
    on("stats-flag", "change", function () { loadStats().catch(function () {}); });
    on("stats-since", "change", function () { loadStats().catch(function () {}); });
    on("refresh-flags", "click", function () { loadFlags().catch(function (e) { toast(e.message); }); });
    on("refresh-releases", "click", function () { loadReleases().catch(function (e) { toast(e.message); }); });
    on("refresh-rules", "click", function () { loadRules().catch(function (e) { toast(e.message); }); });
    on("refresh-experiments", "click", function () { loadExperiments().catch(function (e) { toast(e.message); }); });
    on("refresh-keys", "click", function () { loadKeys().catch(function (e) { toast(e.message); }); });
    on("refresh-stats", "click", function () {
      loadStats().catch(function () { /* loadStats renders inline */ });
    });
    on("stats-view", "click", function (ev) {
      var t = ev && ev.target ? ev.target : null;
      var btn = null;
      if (t) {
        if (t.closest) btn = t.closest("#stats-copy");
        else if (t.id === "stats-copy") btn = t;
      }
      if (!btn) return;
      copyStatsJson();
    });
    on("group-filter", "change", renderFlags);
    on("rule-flag-select", "change", function () { loadRules().catch(function () {}); });
    on("flag-form", "submit", saveFlag);
    on("flag-default", "input", updateFlagDefaultHint);
    on("rule-condition", "input", updateRuleConditionHint);
    on("rule-value", "input", updateRuleValueHint);
    on("exp-variants", "input", updateExpVariantsHint);
    document.addEventListener("click", function (ev) {
      var t = ev && ev.target && ev.target.closest ? ev.target.closest(".json-expand") : null;
      if (t && t.getAttribute) {
        var target = t.getAttribute("data-target");
        if (target) openJsonEditorFor(target);
      }
    });
    on("json-editor-text", "input", updateEditorStatus);
    on("json-editor-format", "click", function () {
      var ta = $("json-editor-text");
      if (!ta) return;
      var parsed = jsonDetail(ta.value);
      if (!parsed.ok) { updateEditorStatus(); return; }
      ta.value = JSON.stringify(parsed.value, null, 2);
      updateEditorStatus();
      try { ta.focus(); } catch (e) { /* best-effort */ }
    });
    on("json-editor-save", "click", function () { closeJsonEditor(true); });
    on("json-editor-cancel", "click", function () { closeJsonEditor(false); });
    updateAllJsonHints();
    on("flag-reset", "click", function () {
      $("flag-id").value = "";
      $("flag-form").reset();
      renderFlagGroupSelect();
      updateFlagDefaultHint();
    });
    on("project-create-form", "submit", createProject);
    on("env-create-form", "submit", createEnv);
    on("group-create-form", "submit", createGroup);
    on("rule-form", "submit", createRule);
    on("experiment-form", "submit", createExperiment);
    on("key-form", "submit", createKey);
    on("key-copy", "click", function () {
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

    on("flag-tbody", "click", function (ev) {
      var t = ev.target;
      var del = t && t.getAttribute && t.getAttribute("data-delete-flag");
      if (del) {
        if (!window.confirm("Delete this flag?")) return;
        deleteFlag(del).catch(function (e) { toast(e.message); });
        return;
      }
      var k = t && t.getAttribute && t.getAttribute("data-stats-flag");
      if (k) {
        var sflag = $("stats-flag");
        if (sflag) {
          var hasOpt = false;
          for (var i = 0; i < sflag.options.length; i++) {
            if (sflag.options[i].value === k) { hasOpt = true; break; }
          }
          if (!hasOpt) {
            var opt = document.createElement("option");
            opt.value = k;
            opt.textContent = k;
            sflag.appendChild(opt);
          }
          sflag.value = k;
        }
        loadStats().catch(function () {});
        var card = $("stats") && $("stats").closest ? $("stats").closest("section") : null;
        if (card && card.scrollIntoView) card.scrollIntoView();
        return;
      }
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
            updateFlagDefaultHint();
            $("flag-result").textContent = "editing " + (f.key || fid);
            break;
          }
        }
      }
    });

    on("release-list", "click", function (ev) {
      var v = ev.target && ev.target.getAttribute && ev.target.getAttribute("data-rollback-version");
      if (!v) return;
      rollback(v, "rollback via admin UI").then(function (out) {
        $("publish-result").textContent = out.status === 200
          ? "rolled back: now v" + out.data.version
          : "rollback failed (" + out.status + "): " + serverMessage(out.data);
        loadReleases().catch(function () {});
      });
    });

    on("key-list", "click", function (ev) {
      var id = ev.target && ev.target.getAttribute && ev.target.getAttribute("data-revoke-key");
      if (id) revokeKey(id);
    });

    on("publish-form", "submit", function (ev) {
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
  window.cwAdmin = {
    state: state, login: login, logout: logout,
    loadFlags: loadFlags, loadReleases: loadReleases, loadStats: loadStats,
    renderStats: renderStats,
    loadProjects: loadProjects, loadEnvs: loadEnvs,
    loadRules: loadRules, loadExperiments: loadExperiments, loadKeys: loadKeys,
    saveFlag: saveFlag, createRule: createRule, createExperiment: createExperiment,
    createKey: createKey, revokeKey: revokeKey,
    createProject: createProject, createEnv: createEnv, createGroup: createGroup,
    deleteFlag: deleteFlag,
    publish: publish, rollback: rollback,
    openJsonEditor: openJsonEditor, closeJsonEditor: closeJsonEditor,
    openJsonEditorFor: openJsonEditorFor,
    updateFlagDefaultHint: updateFlagDefaultHint, updateEditorStatus: updateEditorStatus,
    updateJsonHint: updateJsonHint, updateAllJsonHints: updateAllJsonHints,
    updateRuleConditionHint: updateRuleConditionHint, updateRuleValueHint: updateRuleValueHint,
    updateExpVariantsHint: updateExpVariantsHint,
  };
})();
