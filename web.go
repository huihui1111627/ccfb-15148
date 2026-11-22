package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"txmq/mq"
	"txmq/sim"
)

type overview struct {
	Stats   map[string]any   `json:"stats"`
	Dist    map[string]int   `json:"dist"`
	Txs     []mq.TxInfo      `json:"txs"`
	Checks  []mq.CheckRecord `json:"checks"`
	Alerts  []mq.AlertRecord `json:"alerts"`
	Report  *sim.Report      `json:"report"`
	Running bool             `json:"running"`
}

func serveHTTP(addr string, srvPtr *atomic.Pointer[mq.Server], rec *mq.Recorder, report *sim.Report, db *sim.OrderDB, svc *sim.OrderService) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(pageHTML))
	})
	mux.HandleFunc("/api/overview", func(w http.ResponseWriter, r *http.Request) {
		srv := srvPtr.Load()
		txs := srv.Txs()
		dist := map[string]int{"HALF": 0, "COMMITTED": 0, "ROLLED_BACK": 0}
		for _, t := range txs {
			dist[string(t.State)]++
		}
		avgDwell := int64(0)
		resolved := atomic.LoadInt64(&rec.Committed) + atomic.LoadInt64(&rec.RolledBack)
		if resolved > 0 {
			avgDwell = atomic.LoadInt64(&rec.DwellTotalMs) / resolved
		}
		ov := overview{
			Stats: map[string]any{
				"halfTotal":      atomic.LoadInt64(&rec.HalfTotal),
				"committed":      atomic.LoadInt64(&rec.Committed),
				"rolledBack":     atomic.LoadInt64(&rec.RolledBack),
				"avgDwellMs":     avgDwell,
				"checkRequests":  atomic.LoadInt64(&rec.CheckRequests),
				"checkCommit":    atomic.LoadInt64(&rec.CheckCommit),
				"checkRollback":  atomic.LoadInt64(&rec.CheckRollback),
				"checkUnknown":   atomic.LoadInt64(&rec.CheckUnknown),
				"checkErrors":    atomic.LoadInt64(&rec.CheckErrors),
				"policyApplied":  atomic.LoadInt64(&rec.PolicyApplied),
				"deliveredMsgs":  atomic.LoadInt64(&rec.DeliveredMsgs),
				"serverRestarts": atomic.LoadInt64(&rec.Restarts),
				"producerCrash":  atomic.LoadInt64(&svc.Crashes),
				"quiescent":      srv.Quiescent(),
			},
			Dist:   dist,
			Txs:    txs,
			Checks: rec.Checks(),
			Alerts: rec.Alerts(),
			Report: report,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ov)
	})
	mux.HandleFunc("/api/tx", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		events := rec.EventsFor(id)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     id,
			"state":  srvPtr.Load().StateOf(id),
			"events": events,
		})
	})
	http.ListenAndServe(addr, mux)
}

const pageHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>事务消息 MQ 监控</title>
<style>
  * { box-sizing: border-box; }
  body { font-family: -apple-system, "PingFang SC", sans-serif; margin: 0; background: #0f1420; color: #dde3f0; }
  header { padding: 14px 24px; background: #171e30; border-bottom: 1px solid #2a3450; display:flex; align-items:baseline; gap:16px;}
  header h1 { font-size: 18px; margin: 0; }
  header .sub { color: #7c8bb0; font-size: 13px; }
  main { padding: 20px 24px; display: grid; gap: 18px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 12px; }
  .card { background: #171e30; border: 1px solid #2a3450; border-radius: 10px; padding: 12px 14px; }
  .card .k { font-size: 12px; color: #7c8bb0; }
  .card .v { font-size: 22px; font-weight: 700; margin-top: 4px; }
  .panel { background: #171e30; border: 1px solid #2a3450; border-radius: 10px; padding: 14px 16px; }
  .panel h2 { font-size: 14px; margin: 0 0 10px; color: #9fb2dd; }
  .bar { display: flex; height: 26px; border-radius: 6px; overflow: hidden; font-size: 12px; }
  .bar div { display: flex; align-items: center; justify-content: center; color: #0f1420; font-weight: 600; min-width: 0; overflow: hidden; white-space: nowrap; }
  .b-half { background: #f5b942; } .b-commit { background: #4cc38a; } .b-rb { background: #e5636e; }
  table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #232c46; }
  th { color: #7c8bb0; font-weight: 600; position: sticky; top: 0; background: #171e30; }
  .scroll { max-height: 320px; overflow: auto; }
  .tag { padding: 1px 8px; border-radius: 10px; font-size: 11px; font-weight: 700; }
  .t-HALF { background: #f5b942; color: #20180a; }
  .t-COMMITTED { background: #4cc38a; color: #07281a; }
  .t-ROLLED_BACK { background: #e5636e; color: #2b0a0d; }
  tr.txrow { cursor: pointer; } tr.txrow:hover { background: #1d2740; }
  #timeline div { padding: 5px 8px; border-left: 3px solid #3a4a78; margin: 4px 0 4px 6px; font-size: 12.5px; }
  #timeline .ts { color: #7c8bb0; margin-right: 8px; font-family: monospace; }
  .ok { color: #4cc38a; font-weight: 700; } .bad { color: #e5636e; font-weight: 700; }
  .grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 18px; }
  @media (max-width: 1100px) { .grid2 { grid-template-columns: 1fr; } }
  .alert-item { color: #f5b942; font-size: 12.5px; padding: 3px 0; }
</style>
</head>
<body>
<header>
  <h1>事务消息 MQ 监控</h1>
  <span class="sub">半消息 / 回查 / 投递全链路 · 每 1.5s 自动刷新</span>
  <span class="sub" id="qstate"></span>
</header>
<main>
  <section class="cards" id="cards"></section>

  <section class="panel">
    <h2>半消息状态分布</h2>
    <div class="bar" id="distbar"></div>
    <div style="margin-top:6px;font-size:12px;color:#7c8bb0" id="disttext"></div>
  </section>

  <section class="panel" id="verdict"></section>

  <div class="grid2">
    <section class="panel">
      <h2>事务消息列表(点击查看完整过程)</h2>
      <div class="scroll"><table>
        <thead><tr><th>事务ID</th><th>状态</th><th>回查次数</th><th>决议来源</th><th>停留(ms)</th><th>已投递</th></tr></thead>
        <tbody id="txbody"></tbody>
      </table></div>
    </section>
    <section class="panel">
      <h2>事务过程时间线 <span id="tlid" style="color:#f5b942"></span></h2>
      <div class="scroll" id="timeline"><div style="color:#7c8bb0">点击左侧任意事务查看:发送半消息 → 本地事务 → 提交/回滚/回查 → 投递</div></div>
    </section>
  </div>

  <div class="grid2">
    <section class="panel">
      <h2>回查记录</h2>
      <div class="scroll"><table>
        <thead><tr><th>时间</th><th>事务ID</th><th>实例</th><th>第几次</th><th>结果</th><th>错误</th></tr></thead>
        <tbody id="checkbody"></tbody>
      </table></div>
    </section>
    <section class="panel">
      <h2>告警(超过最大回查次数)</h2>
      <div class="scroll" id="alerts"></div>
    </section>
  </div>
</main>
<script>
const fmtT = ms => ms ? new Date(ms).toLocaleTimeString('zh-CN', {hour12:false}) + '.' + String(ms%1000).padStart(3,'0') : '-';
async function refresh() {
  const ov = await (await fetch('/api/overview')).json();
  const s = ov.stats;
  document.getElementById('qstate').textContent = s.quiescent ? '● 系统已静默(全部决议并投递完成)' : '● 运行中';
  const cards = [
    ['半消息总数', s.halfTotal], ['已提交', s.committed], ['已回滚', s.rolledBack],
    ['平均停留', s.avgDwellMs + ' ms'], ['回查请求', s.checkRequests],
    ['回查:提交', s.checkCommit], ['回查:回滚', s.checkRollback], ['回查:未知', s.checkUnknown],
    ['回查失败', s.checkErrors], ['策略处理+告警', s.policyApplied],
    ['投递消息数', s.deliveredMsgs], ['服务端重启', s.serverRestarts], ['生产者崩溃', s.producerCrash],
  ];
  document.getElementById('cards').innerHTML = cards.map(([k,v]) =>
    '<div class="card"><div class="k">'+k+'</div><div class="v">'+v+'</div></div>').join('');
  const d = ov.dist, total = (d.HALF+d.COMMITTED+d.ROLLED_BACK) || 1;
  const seg = (n, cls, label) => n ? '<div class="'+cls+'" style="width:'+(n/total*100)+'%">'+label+' '+n+'</div>' : '';
  document.getElementById('distbar').innerHTML =
    seg(d.HALF,'b-half','半消息') + seg(d.COMMITTED,'b-commit','已提交') + seg(d.ROLLED_BACK,'b-rb','已回滚');
  const res = d.COMMITTED + d.ROLLED_BACK;
  document.getElementById('disttext').textContent = res ?
    '最终比例 — 提交 ' + (d.COMMITTED/res*100).toFixed(1) + '% : 回滚 ' + (d.ROLLED_BACK/res*100).toFixed(1) + '%' : '';
  const r = ov.report;
  document.getElementById('verdict').innerHTML = r ?
    '<h2>验收结果</h2><div class="'+(r.consistent?'ok':'bad')+'">' +
    (r.consistent ? '✅ 订单库('+r.committedOrders+'笔) 与三个下游消费者完全一致,没有多出也没有缺少'
                  : '❌ 存在不一致,请查看明细') + '</div>' +
    '<div style="font-size:12px;color:#7c8bb0;margin-top:4px">各消费者收到: ' +
    Object.entries(r.perConsumerReceived||{}).map(([k,v])=>k+'='+v).join(', ') +
    ' · 重复投递被幂等去重: ' + r.duplicateDeliveries + ' 次</div>' : '';
  document.getElementById('txbody').innerHTML = ov.txs.slice(0, 400).map(t =>
    '<tr class="txrow" onclick="showTx(\''+t.id+'\')"><td>'+t.id+'</td>' +
    '<td><span class="tag t-'+t.state+'">'+t.state+'</span></td>' +
    '<td>'+t.checks+'</td><td>'+(t.resolvedBy||'-')+'</td><td>'+(t.dwellMs||'-')+'</td>' +
    '<td>'+(t.delivered||[]).join(',')+'</td></tr>').join('');
  document.getElementById('checkbody').innerHTML = (ov.checks||[]).slice(-300).reverse().map(c =>
    '<tr><td>'+fmtT(Date.parse(c.ts))+'</td><td>'+c.txID+'</td><td>'+(c.instance||'-')+'</td>' +
    '<td>'+c.attempt+'</td><td>'+(c.result||'-')+'</td><td>'+(c.err||'')+'</td></tr>').join('');
  document.getElementById('alerts').innerHTML = (ov.alerts||[]).length ?
    ov.alerts.map(a => '<div class="alert-item">⚠ ['+fmtT(Date.parse(a.ts))+'] '+a.msg+'</div>').join('') :
    '<div style="color:#7c8bb0;font-size:12.5px">暂无告警</div>';
}
async function showTx(id) {
  const d = await (await fetch('/api/tx?id='+encodeURIComponent(id))).json();
  document.getElementById('tlid').textContent = id + '  →  ' + (d.state || '?');
  document.getElementById('timeline').innerHTML = (d.events||[]).map(e =>
    '<div><span class="ts">'+fmtT(Date.parse(e.ts))+'</span><b>'+e.kind+'</b> · '+e.detail+'</div>').join('') ||
    '<div style="color:#7c8bb0">无事件</div>';
}
refresh(); setInterval(refresh, 1500);
</script>
</body>
</html>`
