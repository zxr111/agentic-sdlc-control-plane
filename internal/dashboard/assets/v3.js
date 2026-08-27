(() => {
  "use strict";
  const byID = id => document.getElementById(id);
  const escapeHTML = value => String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#039;");
  const empty = text => `<div class="empty-panel"><strong>暂无数据</strong><span>${escapeHTML(text)}</span></div>`;
  const list = (items, render, text) => items?.length ? items.slice(0, 20).map(render).join("") : empty(text);
  const card = (title, status, detail) => `<article class="artifact-card"><div class="panel-title-row"><p class="artifact-name">${escapeHTML(title)}</p><span class="artifact-type">${escapeHTML(status)}</span></div><div class="artifact-meta">${detail}</div></article>`;
  const stats = items => `<div class="v3-stats">${items.map(([label, value]) => `<span>${escapeHTML(label)}<strong>${escapeHTML(value)}</strong></span>`).join("")}</div>`;
  const money = value => `${(Number(value || 0) / 1_000_000).toFixed(4)} 成本单位`;

  function render(data) {
    const v3 = data.v3;
    const r = v3.registry;
    const registry = [["提示词", r.active_prompts], ["模型", r.active_models], ["Agent 配置", r.active_profiles], ["技能", r.active_skills], ["工具", r.active_tools]];
    byID("registry-summary").innerHTML = registry.map(([label, value], index) => `<article class="metric"><div class="metric-icon ${index === 2 ? "metric-icon-running" : "metric-icon-neutral"}">${index + 1}</div><div><span class="metric-label">激活${label}</span><strong class="metric-value">${value}</strong></div></article>`).join("");
    byID("registry-detail").innerHTML = stats(registry.map(([label, value]) => [`激活${label}版本`, value]));
    const u = v3.usage;
    byID("usage").innerHTML = stats([["运行次数", u.runs], ["输入 Token", u.input_tokens.toLocaleString()], ["缓存 Token", u.cached_tokens.toLocaleString()], ["输出 Token", u.output_tokens.toLocaleString()], ["推理 Token", u.reasoning_tokens.toLocaleString()], ["估算成本", money(u.estimated_cost_microunits)], ["平均延迟", `${u.average_latency_ms} ms`]]);
    byID("agent-runs").innerHTML = list(v3.agent_runs, x => card(x.agent_type, `${x.lifecycle_phase} · ${x.status}`, `<span>${x.step_count} 个步骤</span><span>·</span><span>${x.input_tokens + x.output_tokens} Token</span><span>·</span><span>${x.latency_ms} ms</span>`), "工作流触发 Agent 后会在这里显示生命周期记录。");
    byID("routes").innerHTML = list(v3.routes, x => card(`${x.risk_level} 风险`, x.fallback ? "备用路由" : "主路由", `<span>${escapeHTML(x.reason)}</span><span>·</span><span>${money(x.estimated_cost_microunits)}</span>`), "启用模型路由并执行 Agent 后会产生记录。");
    byID("opinions").innerHTML = list(v3.opinions, x => card(x.role, `${x.decision} · ${(x.confidence * 100).toFixed(0)}%`, `<span>${escapeHTML(x.summary)}</span>${x.minority ? "<span>· 少数意见</span>" : ""}`), "多 Agent 需求或架构评审完成后会保留独立意见。");
    byID("model-health").innerHTML = list(v3.model_health, x => card(x.model_key, x.healthy ? "健康" : "异常", `<span>${x.latency_ms} ms</span>${x.error_summary ? `<span>· ${escapeHTML(x.error_summary)}</span>` : ""}`), "模型健康探测尚未产生记录。");
    byID("evaluations").innerHTML = list(v3.evaluations, x => card(x.suite_key, x.status, `<span>${x.shadow ? "影子评测" : "正式评测"}</span><span>·</span><span>平均分 ${Number(x.average_score).toFixed(3)}</span>`), "提交评测运行后会在这里显示分数。");
    const governance = [...(v3.blind_reviews || []).map(x => ({title:`盲评 ${x.submissions}/${x.required_approvals}`,status:x.status,detail:x.id})), ...(v3.canaries || []).map(x => ({title:`${x.candidate_type} 灰度`,status:x.status,detail:`流量 ${x.traffic_percent}%`})), ...(v3.activations || []).map(x => ({title:`${x.registry_type} · ${x.definition_key}`,status:x.action,detail:`操作人 ${x.actor}`}))];
    byID("governance").innerHTML = list(governance, x => card(x.title, x.status, `<span>${escapeHTML(x.detail)}</span>`), "尚无盲评、灰度或版本激活记录。");
    const k = v3.knowledge;
    byID("knowledge").innerHTML = stats([["有效文档", k.active_documents], ["有效版本", k.active_versions], ["知识分块", k.chunks], ["已批准记忆", k.approved_memories], ["待审核记忆", k.candidate_memories]]);
    byID("tool-calls").innerHTML = list(v3.tool_calls, x => card(x.tool_key, x.status, `<span>策略判定：${escapeHTML(x.policy_decision)}</span>${x.error_summary ? `<span>· ${escapeHTML(x.error_summary)}</span>` : ""}`), "Agent 通过工具网关调用工具后会留下审计记录。");
    byID("improvements").innerHTML = list(v3.improvements, x => card(`${x.candidate_type} · ${x.target_key}`, x.status, `<span>${escapeHTML(x.expected_improvement)}</span><span>· 风险：${escapeHTML(x.risk_summary)}</span>`), "失败样本达到聚类条件后会生成待审核改进候选。");
  }

  async function load() {
    byID("refresh-button").disabled = true;
    try {
      const response = await fetch("/api/dashboard", {headers:{Accept:"application/json"}, cache:"no-store"});
      if (!response.ok) throw new Error(`服务返回 ${response.status}`);
      const data = await response.json();
      render(data);
      byID("connection").className = "connection is-online";
      byID("connection-text").textContent = "在线";
      byID("updated-at").textContent = `数据时间：${new Date(data.generated_at).toLocaleString("zh-CN")}`;
    } catch (error) {
      byID("connection").className = "connection is-offline";
      byID("connection-text").textContent = "连接失败";
      byID("updated-at").textContent = error.message;
    } finally { byID("refresh-button").disabled = false; }
  }

  document.querySelectorAll("[data-module]").forEach(button => button.addEventListener("click", () => {
    document.querySelectorAll("[data-module]").forEach(x => x.classList.toggle("is-active", x === button));
    document.querySelectorAll("[data-panel]").forEach(x => x.classList.toggle("is-active", x.dataset.panel === button.dataset.module));
  }));
  byID("refresh-button").addEventListener("click", load);
  load();
  window.setInterval(load, 10000);
})();
