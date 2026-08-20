(() => {
  "use strict";

  const states = [
    ["NEW", "需求接入"], ["INGESTING", "读取来源"], ["REQUIREMENT_ANALYSIS", "需求分析"],
    ["WAITING_REQUIREMENT_REVIEW", "需求门禁"], ["MATERIALIZING_WORK_ITEMS", "创建工作项"],
    ["PRD_GENERATING", "生成 PRD 与测试"], ["WAITING_PRD_AND_TEST_REVIEW", "规划门禁"],
    ["READY_FOR_ARCHITECTURE", "架构就绪"], ["ARCHITECTURE_GENERATING", "架构分析"],
    ["WAITING_ARCHITECTURE_REVIEW", "架构门禁"], ["PLANNING", "实施规划"],
    ["EXECUTING_WORK_ITEMS", "Codex 交付"], ["ASSEMBLING_RELEASE", "组装发布"],
    ["RELEASE_CI_RUNNING", "发布 CI"], ["STAGING_DEPLOYING", "部署测试环境"],
    ["STAGING_VERIFYING", "验证测试环境"], ["WAITING_RELEASE_APPROVAL", "发布门禁"],
    ["PRODUCTION_DEPLOYING", "部署生产环境"], ["OBSERVING", "发布观察"], ["COMPLETED", "已完成"]
  ];

  const stateLabels = {
    NEW: "新建", INGESTING: "读取来源中", REQUIREMENT_ANALYSIS: "需求分析中",
    WAITING_REQUIREMENT_REVIEW: "等待需求审批", MATERIALIZING_WORK_ITEMS: "创建工作项中",
    PRD_GENERATING: "生成 PRD 与测试中", WAITING_PRD_AND_TEST_REVIEW: "等待 PRD 与测试审批",
    READY_FOR_ARCHITECTURE: "架构就绪", ARCHITECTURE_GENERATING: "生成架构中",
    WAITING_ARCHITECTURE_REVIEW: "等待架构审批", PLANNING: "实施规划中",
    EXECUTING_WORK_ITEMS: "执行工作项中", ASSEMBLING_RELEASE: "组装发布中",
    RELEASE_CI_RUNNING: "发布 CI 运行中", STAGING_DEPLOYING: "部署测试环境中",
    STAGING_VERIFYING: "验证测试环境中", WAITING_RELEASE_APPROVAL: "等待发布审批",
    PRODUCTION_DEPLOYING: "部署生产环境中", OBSERVING: "发布观察中", COMPLETED: "已完成",
    PAUSED: "已暂停", CANCELLED: "已取消"
  };

  const gateStates = new Set([
    "WAITING_REQUIREMENT_REVIEW", "WAITING_PRD_AND_TEST_REVIEW",
    "WAITING_ARCHITECTURE_REVIEW", "WAITING_RELEASE_APPROVAL"
  ]);
  const app = {
    data: null,
    selectedID: null,
    filter: "all",
    query: "",
    loading: false,
    timer: null
  };

  const byID = id => document.getElementById(id);
  const escapeHTML = value => String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");

  const shortHash = value => value ? `${value.slice(0, 10)}…${value.slice(-6)}` : "尚未记录";
  const relativeTime = value => {
    const date = new Date(value);
    const seconds = Math.round((date.getTime() - Date.now()) / 1000);
    const formatter = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });
    const ranges = [
      [60, "second"],
      [60, "minute"],
      [24, "hour"],
      [7, "day"],
      [4.345, "week"],
      [12, "month"],
      [Number.POSITIVE_INFINITY, "year"]
    ];
    let amount = seconds;
    for (const [divisor, unit] of ranges) {
      if (Math.abs(amount) < divisor) return formatter.format(Math.round(amount), unit);
      amount /= divisor;
    }
    return date.toLocaleString();
  };

  const formatTime = value => new Intl.DateTimeFormat("zh-CN", {
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(new Date(value));

  function stateClass(state) {
    if (gateStates.has(state)) return "gate";
    if (state === "READY_FOR_ARCHITECTURE" || state === "COMPLETED") return "ready";
    return "active";
  }

  function hasOpenGate(workflow) {
    return Array.isArray(workflow.gates) && workflow.gates.some(gate => gate.status === "OPEN");
  }

  function workflowStateClass(workflow) {
    return hasOpenGate(workflow) ? "gate" : stateClass(workflow.state);
  }

  function filterWorkflows() {
    if (!app.data) return [];
    return app.data.workflows.filter(workflow => {
      const haystack = `${workflow.issue_title} ${workflow.project_path} ${workflow.issue_iid}`.toLowerCase();
      if (app.query && !haystack.includes(app.query.toLowerCase())) return false;
      if (app.filter === "gate") return hasOpenGate(workflow);
      if (app.filter === "ready") return workflow.state === "READY_FOR_ARCHITECTURE" || workflow.state === "COMPLETED";
      if (app.filter === "active") return !hasOpenGate(workflow) && workflow.state !== "COMPLETED";
      return true;
    });
  }

  function renderSummary() {
    const summary = app.data.summary;
    byID("metric-total").textContent = summary.total;
    byID("metric-progress").textContent = summary.in_progress;
    byID("metric-gates").textContent = summary.waiting_gates;
    byID("metric-ready").textContent = summary.ready;
  }

  function renderWorkflowList() {
    const workflows = filterWorkflows();
    byID("workflow-count").textContent = workflows.length;
    if (!workflows.length) {
      byID("workflow-list").innerHTML = `<div class="list-placeholder">${
        app.data.workflows.length ? "没有符合当前筛选条件的工作流。" : "尚未触发工作流。"
      }</div>`;
      return;
    }
    byID("workflow-list").innerHTML = workflows.map(workflow => `
      <button class="workflow-item ${workflow.id === app.selectedID ? "is-selected" : ""}"
        type="button" data-workflow-id="${escapeHTML(workflow.id)}">
        <span class="workflow-item-top">
          <span class="issue-ref">${escapeHTML(workflow.project_path || `项目 ${workflow.gitlab_project_id}`)} · #${workflow.issue_iid}</span>
          <span class="state-pill ${workflowStateClass(workflow)}">${escapeHTML(stateLabels[workflow.state] || workflow.state)}</span>
        </span>
        <span class="workflow-title">${escapeHTML(workflow.issue_title)}</span>
        <span class="workflow-meta">
          <span><span class="state-dot ${workflowStateClass(workflow)}"></span>修订 ${workflow.revision}</span>
          <span>${relativeTime(workflow.updated_at)}</span>
        </span>
      </button>
    `).join("");

    byID("workflow-list").querySelectorAll("[data-workflow-id]").forEach(button => {
      button.addEventListener("click", () => {
        app.selectedID = button.dataset.workflowId;
        renderWorkflowList();
        renderDetail();
      });
    });
  }

  function renderPipeline(workflow) {
    const current = states.findIndex(([state]) => state === workflow.state);
    return states.map(([state, label], index) => {
      const status = index < current ? "is-complete" : index === current ? "is-current" : "";
      const ai = !gateStates.has(state) && state !== "READY_FOR_ARCHITECTURE" && state !== "COMPLETED" ? "is-ai" : "";
      return `<div class="pipeline-step ${status} ${ai}" aria-label="${escapeHTML(label)}: ${
        index < current ? "complete" : index === current ? "current" : "pending"
      }">${escapeHTML(label)}</div>`;
    }).join("");
  }

  function renderGates(workflow) {
    const open = workflow.gates.filter(gate => gate.status === "OPEN");
    if (!open.length) {
      const recent = workflow.gates.slice(0, 3);
      if (!recent.length) return `<div class="empty-panel">尚未开启工程师门禁。</div>`;
      return recent.map(gate => `
        <div class="gate-card">
          <div class="panel-title-row">
            <p class="gate-name">${escapeHTML(gate.type)} Gate · revision ${gate.revision}</p>
            <span class="gate-status ${gate.status.toLowerCase()}">${escapeHTML(gate.status)}</span>
          </div>
          <span class="gate-id">${escapeHTML(gate.id)}</span>
          <div class="gate-actions">
            <span>${gate.decided_at ? `Decided ${relativeTime(gate.decided_at)}` : `Opened ${relativeTime(gate.opened_at)}`}</span>
            ${gate.feedback ? `<span>Feedback recorded</span>` : ""}
          </div>
        </div>
      `).join("");
    }
    return open.map(gate => {
      const command = `/approve gate:${gate.id}`;
      return `
        <div class="gate-card">
          <div class="panel-title-row">
            <p class="gate-name">${escapeHTML(gate.type)} Gate · revision ${gate.revision}</p>
            <span class="gate-status open">ENGINEER ACTION</span>
          </div>
          <span class="gate-id">${escapeHTML(gate.id)}</span>
          <div class="gate-command">
            <code>${escapeHTML(command)}</code>
            <button class="copy-button" type="button" data-copy="${escapeHTML(command)}">Copy</button>
          </div>
          <div class="gate-actions">
            <span>Reviewers: ${gate.reviewer_ids.map(id => `#${id}`).join(", ")}</span>
            <span>Waiting ${relativeTime(gate.opened_at).replace("ago", "")}</span>
          </div>
        </div>
      `;
    }).join("");
  }

  function renderArtifacts(workflow) {
    if (!workflow.artifacts.length) return `<div class="empty-panel">Agent 首次完成后将在此显示制品。</div>`;
    return workflow.artifacts.map(artifact => `
      <div class="artifact-card">
        <div class="panel-title-row">
          <p class="artifact-name">${escapeHTML(artifact.type.replaceAll("_", " "))}</p>
          <span class="artifact-type">v${artifact.version}</span>
        </div>
        <div class="artifact-meta">
          <span>${escapeHTML(artifact.model)}</span>
          <span>·</span>
          <span>${escapeHTML(artifact.prompt)}</span>
          <span>·</span>
          <span>${relativeTime(artifact.generated_at)}</span>
        </div>
      </div>
    `).join("");
  }

  function renderSources(workflow) {
    if (!workflow.sources.length) return `<div class="empty-panel">等待读取 Confluence 需求。</div>`;
    return workflow.sources.map(source => `
      <div class="source-card">
        <a class="source-title" href="${escapeHTML(source.url)}" target="_blank" rel="noopener noreferrer">${escapeHTML(source.title)}</a>
        <div class="source-meta">
          <span>Page ${escapeHTML(source.page_id)}</span>
          <span>v${source.version}</span>
          <span>${source.image_count} visual${source.image_count === 1 ? "" : "s"}</span>
          <span class="hash" title="${escapeHTML(source.content_hash)}">${escapeHTML(shortHash(source.content_hash))}</span>
        </div>
      </div>
    `).join("");
  }

  function describeActivity(activity) {
    const details = activity.details || {};
    if (activity.type === "workflow.transition") {
      return `${details.from || "Unknown"} → ${details.to || "Unknown"}${details.reason ? ` · ${details.reason}` : ""}`;
    }
    if (activity.type === "gate.opened") return `${details.gate_type || "Engineer"} Gate opened`;
    if (activity.type === "gate.decided") return `${details.gate_type || "Engineer"} Gate: ${details.action || "decision recorded"}`;
    if (activity.type === "gate.unauthorized_attempt") return `Unauthorized Gate attempt${details.username ? ` by @${details.username}` : ""}`;
    return Object.keys(details).length ? JSON.stringify(details) : "Recorded by Factory";
  }

  function renderActivity(workflow) {
    if (!workflow.activity.length) return `<div class="empty-panel">尚无审计活动。</div>`;
    return `<div class="timeline">${workflow.activity.slice(0, 12).map(activity => `
      <div class="timeline-item">
        <div class="timeline-type">${escapeHTML(activity.type.replaceAll(".", " · "))}</div>
        <div class="timeline-detail">${escapeHTML(describeActivity(activity))}</div>
        <div class="timeline-detail">${formatTime(activity.created_at)}${activity.actor_id ? ` · actor #${activity.actor_id}` : " · AI Factory"}</div>
      </div>
    `).join("")}</div>`;
  }

  function renderWorkItems(workflow) {
    const items = workflow.work_items || [];
    if (!items.length) return `<div class="empty-panel">需求审批后将在此显示工作项。</div>`;
    return items.map(item => `
      <div class="artifact-card">
        <div class="panel-title-row">
          <p class="artifact-name">${escapeHTML(item.key)} · #${item.issue_iid || "pending"}</p>
          <span class="artifact-type">${escapeHTML(item.state)}</span>
        </div>
        <div class="artifact-meta">
          <span>Engineer #${item.assignee_id || "unassigned"}</span>
          ${item.branch_name ? `<span>·</span><span>${escapeHTML(item.branch_name)}</span>` : ""}
          ${item.dispatch_client ? `<span>·</span><span>Codex ${escapeHTML(item.dispatch_client)}</span>` : ""}
          ${item.merge_request_iid ? `<span>·</span><span>MR !${item.merge_request_iid}</span>` : ""}
        </div>
      </div>
    `).join("");
  }

  function renderAgentRuns(workflow) {
    const runs = workflow.agent_runs || [];
    if (!runs.length) return `<div class="empty-panel">工厂开始生成或质量任务后将在此显示 Agent 运行。</div>`;
    return runs.slice(0, 12).map(run => `
      <div class="artifact-card">
        <div class="panel-title-row">
          <p class="artifact-name">${escapeHTML(run.agent_type)} · run ${run.run_number}</p>
          <span class="artifact-type">${escapeHTML(run.status)}</span>
        </div>
        <div class="artifact-meta">
          <span>${escapeHTML(run.model || "Factory")}</span>
          ${run.work_item_id ? `<span>·</span><span>${escapeHTML(run.work_item_id)}</span>` : ""}
          <span>·</span><span>${relativeTime(run.started_at)}</span>
        </div>
      </div>
    `).join("");
  }

  function renderDetail() {
    if (!app.data) return;
    const workflow = app.data.workflows.find(item => item.id === app.selectedID);
    if (!workflow) {
      byID("workflow-detail").innerHTML = `
        <section class="empty-state">
          <div class="empty-visual" aria-hidden="true"><span></span><span></span><span></span></div>
          <p class="eyebrow">${app.data.workflows.length ? "尚未选择工作流" : "软件工厂已就绪"}</p>
          <h2>${app.data.workflows.length ? "选择工作流查看完整交付过程" : "等待第一个需求接入事件"}</h2>
          <p>${app.data.workflows.length
            ? "Inspect Agent progress, Engineer Gates, traceability, failures, and audit history."
            : "Create an open GitLab Issue with a Confluence link and the automation::enabled label."}</p>
        </section>`;
      return;
    }

    const gatePanelClass = workflow.gates.some(gate => gate.status === "OPEN") ? "panel-gate" : "";
    byID("workflow-detail").innerHTML = `
      <header class="workflow-header">
        <div>
          <span class="state-pill ${stateClass(workflow.state)}">${escapeHTML(stateLabels[workflow.state] || workflow.state)}</span>
          <h2>${escapeHTML(workflow.issue_title)}</h2>
          <div class="detail-meta">
            <span>${escapeHTML(workflow.project_path || `项目 ${workflow.gitlab_project_id}`)} #${workflow.issue_iid}</span>
            <span>修订 ${workflow.revision}</span>
            <span>Updated ${relativeTime(workflow.updated_at)}</span>
            <span class="hash">Source ${escapeHTML(shortHash(workflow.source_hash))}</span>
          </div>
        </div>
        <div class="header-actions">
          ${workflow.issue_url ? `<a class="button button-primary" href="${escapeHTML(workflow.issue_url)}" target="_blank" rel="noopener noreferrer">打开 GitLab Issue</a>` : ""}
        </div>
      </header>

      <section class="journey" aria-label="Workflow state machine">
        <div class="journey-label">
          <h3>Delivery journey</h3>
          <span>AI advances · Engineer authorizes</span>
        </div>
        <div class="pipeline">${renderPipeline(workflow)}</div>
      </section>

      <div class="detail-grid">
        <div class="detail-column">
          <section class="panel ${gatePanelClass}">
            <div class="panel-title-row">
              <h3>Engineer Gates</h3>
              <span>${workflow.gates.filter(gate => gate.status === "OPEN").length} open</span>
            </div>
            ${renderGates(workflow)}
          </section>
          <section class="panel">
            <div class="panel-title-row">
              <h3>Agent artifacts</h3>
              <span>${workflow.artifacts.length} latest</span>
            </div>
            ${renderArtifacts(workflow)}
          </section>
          <section class="panel">
            <div class="panel-title-row">
              <h3>Immutable sources</h3>
              <span>${workflow.sources.length} page${workflow.sources.length === 1 ? "" : "s"}</span>
            </div>
            ${renderSources(workflow)}
          </section>
        </div>
        <div class="detail-column">
          <section class="panel">
            <div class="panel-title-row">
              <h3>Codex work items</h3>
              <span>${(workflow.work_items || []).length} tracked</span>
            </div>
            ${renderWorkItems(workflow)}
          </section>
          <section class="panel">
            <div class="panel-title-row">
              <h3>Agent runs</h3>
              <span>${(workflow.agent_runs || []).length} recent</span>
            </div>
            ${renderAgentRuns(workflow)}
          </section>
          <section class="panel">
            <div class="panel-title-row">
              <h3>Audit timeline</h3>
              <span>Latest ${Math.min(workflow.activity.length, 12)}</span>
            </div>
            ${renderActivity(workflow)}
          </section>
        </div>
      </div>
    `;

    byID("workflow-detail").querySelectorAll("[data-copy]").forEach(button => {
      button.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(button.dataset.copy);
          showToast("Gate command copied. Paste it into the GitLab Issue.");
        } catch {
          showToast("Clipboard access was denied.");
        }
      });
    });
  }

  function renderOperations() {
    const queue = app.data.queues;
    const values = [
      ["待处理事件", queue.events_ready], ["处理中事件", queue.events_processing], ["事件死信", queue.events_dead],
      ["待发送消息", queue.outbox_ready], ["发送中消息", queue.outbox_processing], ["消息死信", queue.outbox_dead]
    ];
    byID("queue-grid").innerHTML = values.map(([label, value]) => `
      <div class="queue-item">
        <span>${label}</span>
        <strong>${value}</strong>
      </div>
    `).join("");

    const dead = queue.events_dead + queue.outbox_dead;
    const status = byID("operations-status");
    status.className = `operations-status ${dead ? "is-unhealthy" : "is-healthy"}`;
    status.textContent = dead ? `${dead} 条死信需要处理` : "队列健康";

    if (!app.data.failures.length) {
      byID("failure-list").innerHTML = "";
      return;
    }
    byID("failure-list").innerHTML = `
      <table class="failure-table">
        <thead><tr><th>Queue</th><th>Type</th><th>Attempts</th><th>Last error</th></tr></thead>
        <tbody>${app.data.failures.map(failure => `
          <tr>
            <td><span class="queue-status dead">${escapeHTML(failure.kind)}</span></td>
            <td>${escapeHTML(failure.type)}</td>
            <td>${failure.attempts}</td>
            <td>${escapeHTML(failure.last_error)}</td>
          </tr>
        `).join("")}</tbody>
      </table>`;
  }

  function compactList(items, renderItem, emptyText) {
    if (!items || !items.length) return `<div class="empty-panel">${escapeHTML(emptyText)}</div>`;
    return items.slice(0, 8).map(renderItem).join("");
  }

  function renderV3() {
    const v3 = app.data.v3;
    if (!v3) return;
    const registry = v3.registry;
    const registryValues = [
      ["激活提示词", registry.active_prompts], ["激活模型", registry.active_models],
      ["Agent 配置", registry.active_profiles], ["激活技能", registry.active_skills],
      ["激活工具", registry.active_tools]
    ];
    byID("v3-registry").innerHTML = registryValues.map(([label, value]) => `<div class="queue-item"><span>${label}</span><strong>${value}</strong></div>`).join("");
    const usage = v3.usage;
    byID("v3-usage-title").textContent = `${usage.runs} 次运行`;
    byID("v3-usage").innerHTML = `<div class="v3-stats">
      <span>输入 Token<strong>${usage.input_tokens.toLocaleString()}</strong></span>
      <span>缓存 Token<strong>${usage.cached_tokens.toLocaleString()}</strong></span>
      <span>输出 Token<strong>${usage.output_tokens.toLocaleString()}</strong></span>
      <span>推理 Token<strong>${usage.reasoning_tokens.toLocaleString()}</strong></span>
      <span>估算成本（微单位）<strong>${usage.estimated_cost_microunits.toLocaleString()}</strong></span>
      <span>平均延迟<strong>${usage.average_latency_ms} ms</strong></span></div>`;
    byID("v3-agent-runs").innerHTML = compactList(v3.agent_runs, run => `<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(run.agent_type)}</p><span class="artifact-type">${escapeHTML(run.lifecycle_phase)}</span></div><div class="artifact-meta"><span>${run.step_count} 个步骤</span><span>·</span><span>${run.input_tokens + run.output_tokens} Tokens</span><span>·</span><span>${run.latency_ms} ms</span></div></div>`, "暂无 Agent 生命周期记录。");
    const knowledge = v3.knowledge;
    byID("v3-knowledge").innerHTML = `<div class="v3-stats">
      <span>有效文档<strong>${knowledge.active_documents}</strong></span><span>有效版本<strong>${knowledge.active_versions}</strong></span>
      <span>知识分块<strong>${knowledge.chunks}</strong></span><span>已批准记忆<strong>${knowledge.approved_memories}</strong></span>
      <span>候选记忆<strong>${knowledge.candidate_memories}</strong></span></div>`;
    byID("v3-routes").innerHTML = compactList(v3.routes, route => `<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(route.risk_level)} 风险路由</p><span class="artifact-type">${route.fallback ? "降级" : "主路由"}</span></div><div class="artifact-meta"><span>${escapeHTML(route.reason)}</span><span>·</span><span>${route.estimated_cost_microunits} 微单位</span></div></div>`, "暂无模型路由记录。");
    byID("v3-opinions").innerHTML = compactList(v3.opinions, opinion => `<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(opinion.role)}</p><span class="artifact-type">${escapeHTML(opinion.decision)} · ${(opinion.confidence * 100).toFixed(0)}%</span></div><div class="artifact-meta"><span>${escapeHTML(opinion.summary)}</span>${opinion.minority ? "<span>· 少数意见</span>" : ""}</div></div>`, "暂无多 Agent 意见。");
    byID("v3-evaluations").innerHTML = compactList(v3.evaluations, run => `<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(run.suite_key)}</p><span class="artifact-type">${escapeHTML(run.status)}</span></div><div class="artifact-meta"><span>${run.shadow ? "影子评测" : "正式评测"}</span><span>·</span><span>均分 ${Number(run.average_score).toFixed(3)}</span></div></div>`, "暂无评测运行。");
    const governance=[...(v3.blind_reviews||[]).map(item=>({name:`盲评 ${item.submissions}/${item.required_approvals}`,status:item.status,detail:item.id})),...(v3.canaries||[]).map(item=>({name:`${item.candidate_type} Canary`,status:item.status,detail:`流量 ${item.traffic_percent}%`})),...(v3.activations||[]).map(item=>({name:`${item.registry_type} · ${item.definition_key}`,status:item.action,detail:`审批人 ${item.actor}`}))];
    byID("v3-governance").innerHTML=compactList(governance,item=>`<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(item.name)}</p><span class="artifact-type">${escapeHTML(item.status)}</span></div><div class="artifact-meta"><span>${escapeHTML(item.detail)}</span></div></div>`,"暂无晋升治理记录。");
    byID("v3-model-health").innerHTML=compactList(v3.model_health,item=>`<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(item.model_key)}</p><span class="artifact-type">${item.healthy?"健康":"异常"}</span></div><div class="artifact-meta"><span>${item.latency_ms} ms</span>${item.error_summary?`<span>· ${escapeHTML(item.error_summary)}</span>`:""}</div></div>`,"暂无模型健康记录。");
    byID("v3-tools").innerHTML = compactList(v3.tool_calls, call => `<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(call.tool_key)}</p><span class="artifact-type">${escapeHTML(call.status)}</span></div><div class="artifact-meta"><span>策略：${escapeHTML(call.policy_decision)}</span>${call.error_summary ? `<span>· ${escapeHTML(call.error_summary)}</span>` : ""}</div></div>`, "暂无工具调用。");
    byID("v3-improvements").innerHTML = compactList(v3.improvements, item => `<div class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(item.target_key)}</p><span class="artifact-type">${escapeHTML(item.status)}</span></div><div class="artifact-meta"><span>${escapeHTML(item.expected_improvement)}</span><span>· 风险：${escapeHTML(item.risk_summary)}</span></div></div>`, "暂无持续改进候选。");
    const status = byID("v3-status");
    status.className = "operations-status is-healthy";
    status.textContent = "数据已同步";
  }

  function render() {
    renderSummary();
    if (!app.selectedID && app.data.workflows.length) app.selectedID = app.data.workflows[0].id;
    if (app.selectedID && !app.data.workflows.some(workflow => workflow.id === app.selectedID)) {
      app.selectedID = app.data.workflows[0]?.id || null;
    }
    renderWorkflowList();
    renderDetail();
    renderOperations();
    renderV3();
  }

  function setConnection(state, label) {
    const element = byID("connection");
    element.className = `connection ${state ? `is-${state}` : ""}`;
    byID("connection-text").textContent = label;
  }

  async function load() {
    if (app.loading) return;
    app.loading = true;
    byID("refresh-button").disabled = true;
    setConnection("", "同步中");
    try {
      const response = await fetch("/api/dashboard", {
        headers: { Accept: "application/json" },
        cache: "no-store"
      });
      if (!response.ok) throw new Error(`Dashboard returned ${response.status}`);
      app.data = await response.json();
      render();
      setConnection("online", "在线");
      byID("updated-at").textContent = `更新于${relativeTime(app.data.generated_at)}`;
    } catch (error) {
      setConnection("offline", "连接已断开");
      byID("updated-at").textContent = error.message;
      showToast("无法刷新软件工厂数据。");
    } finally {
      app.loading = false;
      byID("refresh-button").disabled = false;
    }
  }

  function showToast(message) {
    const toast = byID("toast");
    toast.textContent = message;
    toast.classList.add("is-visible");
    window.clearTimeout(showToast.timer);
    showToast.timer = window.setTimeout(() => toast.classList.remove("is-visible"), 2600);
  }

  byID("refresh-button").addEventListener("click", load);
  byID("search-input").addEventListener("input", event => {
    app.query = event.target.value.trim();
    renderWorkflowList();
  });
  document.querySelectorAll("[data-filter]").forEach(button => {
    button.addEventListener("click", () => {
      app.filter = button.dataset.filter;
      document.querySelectorAll("[data-filter]").forEach(item => item.classList.toggle("is-active", item === button));
      renderWorkflowList();
    });
  });

  load();
  app.timer = window.setInterval(load, 10000);
})();
