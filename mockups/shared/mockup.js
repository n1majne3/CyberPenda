/* CyberPenda UI mockups — shared runtime: sidebar shell, annotation layer, widgets */

/* ---------- Sidebar (Direction A redesign) ----------
   Changes vs. current UI:
   - status dot per session/task (Runtime Activity Indicator at a glance)
   - duplicate names disambiguated by relative time + state
   - search filter, section counts, hover actions                    */

const SIDEBAR_SESSIONS = [
  { name: "just say hi", time: "2 分钟前", state: "busy", note: "运行中" },
  { name: "just say hi", time: "昨天 21:14", state: "idle", note: "空闲" },
  { name: "just say hi", time: "3 天前", state: "off", note: "已停止" },
  { name: "第一步什么都不要做：先连接附件的 VPN", time: "昨天 22:23", state: "off", note: "已停止" },
  { name: "第一步什么都不要做：先连…", time: "8 月 26 日", state: "off", note: "已停止" },
];

const SIDEBAR_PROJECTS = [
  { name: "ASEAN", open: true, tasks: [
    { name: "Hidden_Margin 题目分值：17…", state: "off" },
    { name: "InvoiceLink Gateway 题目分…", state: "busy" },
    { name: "release_chain 题目分值：500…", state: "off" },
    { name: "Sentinel AgentOps 题目分值…", state: "off" },
    { name: "finance_tool 题目分值：350…", state: "idle" },
  ]},
  { name: "cybench" }, { name: "tsecbench" }, { name: "nssctf" },
  { name: "naactf" }, { name: "test" },
  { name: "CyberGym interactive arvo…" },
  { name: "CyberGym assisted arvo:78…" },
  { name: "CyberGym interactive arvo…" },
];

function stateDot(state, withPulse) {
  const map = {
    busy: ["hsl(var(--success))", "运行中"],
    idle: ["hsl(var(--info))", "空闲"],
    off: ["hsl(0 0% 70%)", "离线/停止"],
    orphan: ["hsl(var(--warning))", "状态未知"],
  };
  const [color, label] = map[state] || map.off;
  const pulse = state === "busy" && withPulse ? " dot-live" : "";
  return `<span class="dot${pulse}" style="background:${color}" title="${label}"></span>`;
}

function renderSidebar(active) {
  const sessionRows = SIDEBAR_SESSIONS.map((s) => `
    <a href="#" style="display:flex;align-items:flex-start;gap:8px;border-radius:6px;padding:6px 8px;text-decoration:none;color:inherit" onmouseover="this.style.background='hsl(var(--sidebar-accent))'" onmouseout="this.style.background=''">
      <span style="margin-top:6px;flex:none">${stateDot(s.state, true)}</span>
      <span style="min-width:0;flex:1">
        <span style="display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px;line-height:1.3">${s.name}</span>
        <span style="display:block;font-size:11px;color:hsl(var(--muted-foreground));line-height:1.3;margin-top:2px">${s.time} · ${s.note}</span>
      </span>
      <i data-lucide="more-horizontal" style="width:16px;height:16px;margin-top:4px;flex:none;opacity:0" onmouseover="this.style.opacity='0.6'" onmouseout="this.style.opacity='0'"></i>
    </a>`).join("");

  const projectRows = SIDEBAR_PROJECTS.map((p) => {
    const tasks = (p.tasks || []).map((t) => `
      <a href="#" style="display:flex;align-items:center;gap:8px;border-radius:6px;padding:4px 8px 4px 28px;text-decoration:none;color:inherit" onmouseover="this.style.background='hsl(var(--sidebar-accent))'" onmouseout="this.style.background=''">
        <span style="flex:none">${stateDot(t.state, true)}</span>
        <span style="min-width:0;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px">${t.name}</span>
        <i data-lucide="more-horizontal" style="width:14px;height:14px;flex:none;opacity:0" onmouseover="this.style.opacity='0.6'" onmouseout="this.style.opacity='0'"></i>
      </a>`).join("");
    return `
      <div>
        <a href="#" style="display:flex;align-items:center;gap:6px;border-radius:6px;padding:6px 8px;font-size:13px;text-decoration:none;color:inherit;${p.open ? "font-weight:500;" : ""}" onmouseover="this.style.background='hsl(var(--sidebar-accent))'" onmouseout="this.style.background=''">
          <i data-lucide="chevron-right" style="width:14px;height:14px;flex:none;${p.open ? "transform:rotate(90deg);" : ""}"></i>
          <i data-lucide="folder" style="width:14px;height:14px;flex:none;opacity:0.7"></i>
          <span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${p.name}</span>
        </a>
        ${p.open ? `<div style="display:flex;flex-direction:column;gap:2px">${tasks}</div>` : ""}
      </div>`;
  }).join("");

  const settingsRows = [
    ["settings-2", "Runtime profiles", "profiles"],
    ["server", "Model providers", "providers"],
    ["key-round", "Credentials", "credentials"],
    ["book-open", "Skills", "skills"],
  ].map(([icon, label, key]) => `
    <a href="#" style="display:flex;align-items:center;gap:8px;border-radius:6px;padding:6px 8px;font-size:13px;text-decoration:none;color:inherit;${active === key ? "background:hsl(var(--sidebar-accent));font-weight:500;" : ""}" onmouseover="this.style.background='hsl(var(--sidebar-accent))'" onmouseout="this.style.background='${active === key ? "hsl(var(--sidebar-accent))" : ""}'">
      <i data-lucide="${icon}" style="width:16px;height:16px;opacity:0.7"></i>${label}
    </a>`).join("");

  return `
  <aside style="display:flex;height:100vh;width:248px;flex:none;flex-direction:column;border-right:1px solid hsl(var(--sidebar-border));background:hsl(var(--sidebar))">
    <div style="display:flex;align-items:center;gap:8px;padding:16px 16px 12px">
      <span style="display:flex;height:24px;width:24px;align-items:center;justify-content:center;border-radius:6px;background:hsl(var(--primary));color:hsl(var(--primary-foreground))">
        <i data-lucide="shield-half" style="width:14px;height:14px"></i>
      </span>
      <span style="font-size:14px;font-weight:600;letter-spacing:-0.01em">CyberPenda</span>
    </div>
    <div style="padding:0 12px 8px" data-note="侧边栏新增搜索过滤：session / project 变多后不再依赖 'Show more' 翻页">
      <div style="display:flex;align-items:center;gap:8px;border-radius:6px;border:1px solid hsl(var(--sidebar-border));background:hsl(var(--card));padding:0 8px;height:32px">
        <i data-lucide="search" style="width:14px;height:14px;color:hsl(var(--muted-foreground))"></i>
        <input placeholder="Filter…" style="width:100%;background:transparent;font-size:12px;outline:none;border:none;color:inherit" />
        <kbd style="font-family:var(--font-geist-mono);font-size:10px;color:hsl(var(--muted-foreground));border:1px solid hsl(var(--border));border-radius:4px;padding:0 4px">/</kbd>
      </div>
    </div>
    <nav style="flex:1;overflow-y:auto;padding:0 12px 12px">
      <div style="display:flex;align-items:center;justify-content:space-between;padding:8px 8px 4px">
        <span class="section-label">Non-project</span>
        <span style="display:flex;align-items:center;gap:4px">
          <span style="font-size:10px;color:hsl(var(--muted-foreground))">5</span>
          <i data-lucide="plus" style="width:16px;height:16px;color:hsl(var(--muted-foreground))"></i>
        </span>
      </div>
      <div style="display:flex;flex-direction:column;gap:2px" data-note="每个 Session 带 Runtime 活动状态点（绿脉冲=运行中 / 蓝=空闲 / 灰=已停止），重名 session 用相对时间区分">
        ${sessionRows}
      </div>
      <button style="margin-top:4px;padding:0 8px;font-size:12px;color:hsl(var(--muted-foreground));background:none;border:none;cursor:pointer" onmouseover="this.style.color='hsl(var(--foreground))'" onmouseout="this.style.color='hsl(var(--muted-foreground))'">Show more</button>

      <div style="display:flex;align-items:center;justify-content:space-between;padding:16px 8px 4px">
        <span class="section-label">Projects</span>
        <span style="display:flex;align-items:center;gap:4px">
          <span style="font-size:10px;color:hsl(var(--muted-foreground))">9</span>
          <i data-lucide="plus" style="width:16px;height:16px;color:hsl(var(--muted-foreground))"></i>
        </span>
      </div>
      <div style="display:flex;flex-direction:column;gap:2px">${projectRows}</div>

      <div style="padding:16px 8px 4px"><span class="section-label">Settings</span></div>
      <div style="display:flex;flex-direction:column;gap:2px">${settingsRows}</div>
    </nav>
    <div style="display:flex;align-items:center;justify-content:space-between;border-top:1px solid hsl(var(--sidebar-border));padding:10px 16px">
      <span style="font-size:12px;color:hsl(var(--muted-foreground))">Theme</span>
      <i data-lucide="moon" style="width:16px;height:16px;color:hsl(var(--muted-foreground))"></i>
    </div>
  </aside>`;
}

/* ---------- Annotation layer ---------- */

function collectNotes() {
  const items = [];
  document.querySelectorAll("[data-note]").forEach((el) => {
    items.push({ el, text: el.getAttribute("data-note") });
  });
  return items;
}

function layoutNotes() {
  document.querySelectorAll(".note-badge").forEach((b) => b.remove());
  collectNotes().forEach(({ el }, i) => {
    const badge = document.createElement("i");
    badge.className = "note-badge";
    badge.textContent = String(i + 1);
    const r = el.getBoundingClientRect();
    const top = r.top + window.scrollY;
    const left = r.left + window.scrollX;
    badge.style.top = `${top}px`;
    badge.style.left = `${left}px`;
    document.body.appendChild(badge);
  });
  const panel = document.getElementById("note-panel");
  if (panel) {
    panel.innerHTML = `
      <div class="section-label" style="margin-bottom:6px">本页改动说明</div>
      <ol class="m-0 p-0 list-none">${collectNotes().map(({ text }, i) =>
        `<li><span class="n">${i + 1}</span><span>${text}</span></li>`).join("")}
      </ol>`;
  }
}

function toggleNotes(force) {
  const on = force !== undefined ? force : !document.body.classList.contains("notes-on");
  document.body.classList.toggle("notes-on", on);
  if (on) layoutNotes();
}

/* ---------- Boot ---------- */

function bootMockup(opts) {
  const shell = document.getElementById("sidebar-slot");
  if (shell) shell.innerHTML = renderSidebar(opts && opts.active);

  // accordion behavior
  document.querySelectorAll(".acc-head").forEach((h) =>
    h.addEventListener("click", () => h.closest(".acc").classList.toggle("open")));

  // annotation UI
  const btn = document.createElement("button");
  btn.id = "notes-toggle";
  btn.innerHTML = '<i data-lucide="list-ordered" class="w-4 h-4"></i> 改动说明 (A)';
  btn.addEventListener("click", () => toggleNotes());
  document.body.appendChild(btn);
  const panel = document.createElement("div");
  panel.id = "note-panel";
  document.body.appendChild(panel);
  window.addEventListener("keydown", (e) => {
    if (e.key.toLowerCase() === "a" && !/input|textarea/i.test(document.activeElement.tagName)) toggleNotes();
  });
  window.addEventListener("resize", () => { if (document.body.classList.contains("notes-on")) layoutNotes(); });

  const banner = document.createElement("div");
  banner.className = "mock-banner";
  banner.textContent = "静态 mockup · Direction A 精炼极简 · 仅用于设计评审";
  document.body.appendChild(banner);

  if (window.lucide) window.lucide.createIcons();

  // default: notes on for first view
  toggleNotes(true);
}

document.addEventListener("DOMContentLoaded", () => {
  if (window.MOCKUP) bootMockup(window.MOCKUP);
});
