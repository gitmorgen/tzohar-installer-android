// Wizard front-end. Polls /api/state and drives the two actions (load
// enrollment, run install). The per-run token comes from our own URL.
const token = new URLSearchParams(location.search).get("t") || "";

function api(path, opts = {}) {
  const url = path + (path.includes("?") ? "&" : "?") + "t=" + encodeURIComponent(token);
  return fetch(url, opts).then((r) => r.json());
}

const $ = (id) => document.getElementById(id);

const STEP_LABELS = {
  install: "Install the app",
  configure: "Enable accessibility, battery exemption, updates",
  enroll: "Send the install key and start the agent",
};
const TICK = { pending: "○", active: "◐", done: "✓", failed: "✕" };

// --- actions ---------------------------------------------------------------

$("enroll-btn").addEventListener("click", loadEnrollment);
$("enroll-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") loadEnrollment();
});

async function loadEnrollment() {
  const input = $("enroll-input").value.trim();
  $("enroll-err").textContent = "";
  $("enroll-ok").textContent = "";
  if (!input) return;
  $("enroll-btn").disabled = true;
  try {
    const res = await api("/api/enroll", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ input }),
    });
    if (res.error) {
      $("enroll-err").textContent = res.error;
    } else {
      render(res);
    }
  } catch (e) {
    $("enroll-err").textContent = "Could not reach the installer service.";
  } finally {
    $("enroll-btn").disabled = false;
  }
}

$("run-btn").addEventListener("click", async () => {
  $("run-btn").disabled = true;
  const res = await api("/api/install", { method: "POST" });
  if (res && res.error) alert(res.error);
});

// --- rendering -------------------------------------------------------------

function render(s) {
  // Step 1: enrollment
  const eb = $("enroll-badge");
  if (s.enrollment) {
    eb.textContent = "loaded";
    eb.className = "badge ok";
    const v = s.enrollment.artefact && s.enrollment.artefact.version;
    $("enroll-ok").textContent =
      "Enrollment loaded" + (v ? " · agent " + v : "") + ".";
    $("enroll-input").value = $("enroll-input").value; // keep
  } else {
    eb.textContent = "needed";
    eb.className = "badge";
  }

  // Step 2: device
  renderDevice(s);

  // Step 3: run button + steps
  const canRun =
    s.adb_ready && s.device && s.device.state === "device" && s.enrollment && !s.busy && !s.finished;
  $("run-btn").disabled = !canRun;
  const rb = $("run-badge");
  if (s.finished) {
    rb.textContent = "done";
    rb.className = "badge ok";
  } else if (s.busy) {
    rb.textContent = "working…";
    rb.className = "badge";
  } else if (s.run_error) {
    rb.textContent = "failed";
    rb.className = "badge err";
  } else {
    rb.textContent = "ready";
    rb.className = "badge";
  }

  renderSteps(s);

  $("done-banner").hidden = !s.finished;
  if (s.device && s.device.state === "device" && s.finished) {
    // Android 10 hint: capture needs one MediaProjection tap there.
    $("done-note").textContent = "";
  }

  $("log").textContent = s.log || "";
  $("log").scrollTop = $("log").scrollHeight;
}

function renderDevice(s) {
  const badge = $("device-badge");
  const box = $("device-status");
  if (!s.adb_ready) {
    badge.textContent = "starting";
    badge.className = "badge";
    box.innerHTML = line("warn", s.adb_error ? "ADB error: " + s.adb_error : "Setting up ADB…");
    return;
  }
  if (!s.device) {
    badge.textContent = "waiting";
    badge.className = "badge";
    box.innerHTML = line("", "No phone detected. Turn on USB debugging and plug it in.");
    return;
  }
  if (s.device.state === "unauthorized") {
    badge.textContent = "allow it";
    badge.className = "badge";
    box.innerHTML = line("warn", "Phone connected but not authorized — tap <b>Allow</b> on the phone's USB-debugging prompt.");
    return;
  }
  if (s.device.state !== "device") {
    badge.textContent = s.device.state;
    badge.className = "badge err";
    box.innerHTML = line("err", "Device state: " + s.device.state + ". Reconnect the cable.");
    return;
  }
  badge.textContent = "connected";
  badge.className = "badge ok";
  const name = s.device.model || s.device.serial;
  box.innerHTML = line("ok", "Connected: <b>" + esc(name) + "</b>");
}

function renderSteps(s) {
  const ul = $("steps");
  const order = s.step_order || ["install", "configure", "enroll"];
  const show = s.busy || s.finished || s.run_error;
  ul.innerHTML = show
    ? order
        .map((k) => {
          const st = (s.steps && s.steps[k]) || "pending";
          return (
            '<li class="st-' + st + '"><span class="tick">' + TICK[st] +
            '</span><span class="label">' + STEP_LABELS[k] + "</span></li>"
          );
        })
        .join("")
    : "";
}

function line(kind, html) {
  return '<div class="dev-line"><span class="dot ' + kind + '"></span><span>' + html + "</span></div>";
}
function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// --- poll loop -------------------------------------------------------------

async function tick() {
  try {
    render(await api("/api/state"));
  } catch (e) {
    /* transient; try again next tick */
  }
}
setInterval(tick, 1500);
tick();
