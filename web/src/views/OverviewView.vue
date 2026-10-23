<script setup lang="ts">
import { computed } from 'vue'
import { useConsole } from '../console'

const {
  status,
  disconnected,
  conflict,
  dirty,
  poolHealth,
  serviceLabel,
  serviceTone,
  go,
  probe,
  service,
  showPool,
} = useConsole()

const totals = computed(() =>
  poolHealth.value.reduce(
    (t, p) => ({ total: t.total + p.total, healthy: t.healthy + p.healthy }),
    { total: 0, healthy: 0 },
  ),
)
const live = computed(() => !!status.value?.active && !disconnected.value)
</script>

<template>
  <div class="page-stack">
    <section class="stat-grid" aria-label="运行概况">
      <article class="stat" :class="`tone-${serviceTone}`">
        <span class="stat-label">转发服务</span>
        <strong class="stat-value"><span class="status-dot" />{{ serviceLabel }}</strong>
      </article>
      <article class="stat">
        <span class="stat-label">出口</span>
        <strong class="stat-value">{{ status?.activeProfile ?? '—' }}</strong>
      </article>
      <article class="stat">
        <span class="stat-label">连接</span>
        <strong class="stat-value num" data-testid="stat-connections">{{
          live ? (status?.connections ?? 0) : '—'
        }}</strong>
      </article>
      <article class="stat">
        <span class="stat-label">健康节点</span>
        <strong class="stat-value num" data-testid="stat-health"
          >{{ live ? totals.healthy : '—' }}<small> / {{ totals.total }}</small></strong
        >
      </article>
    </section>

    <div class="overview-grid">
      <section class="card">
        <header class="card-header">
          <h2>出口</h2>
          <el-button size="small" @click="go('nodes')">管理节点</el-button>
        </header>
        <ul class="pool-list">
          <li v-for="p in poolHealth" :key="p.profile.name" class="pool-item">
            <div class="pool-name">
              <strong>{{ p.profile.name }}</strong>
              <span v-if="p.profile.name === status?.activeProfile" class="badge badge-primary"
                >当前</span
              >
              <span
                v-for="e in status?.entries?.filter((v) => v.profile === p.profile.name) ?? []"
                :key="e.name"
                class="badge"
                :title="`附加入口 ${e.addr}${e.available ? '' : ' · 出口不可用'}`"
                >入口 {{ e.name }}</span
              >
            </div>
            <template v-if="p.profile.type === 'pool'">
              <div
                class="health-bar"
                role="img"
                :aria-label="`${p.profile.name} 健康 ${p.healthy} / ${p.total}`"
                :title="`${p.healthy} 正常 · ${p.failed} 故障 · ${p.profile.probe?.type?.toUpperCase() ?? '未设置'} 探测`"
              >
                <span
                  class="health-ok"
                  :style="{ width: live && p.total ? (p.healthy / p.total) * 100 + '%' : '0' }"
                /><span
                  class="health-bad"
                  :style="{ width: live && p.total ? (p.failed / p.total) * 100 + '%' : '0' }"
                />
              </div>
              <span class="pool-count mono">{{ live ? p.healthy : '—' }} / {{ p.total }}</span>
              <div class="pool-actions">
                <el-button link type="primary" @click="showPool(p.profile.name)">查看节点</el-button
                ><el-button
                  link
                  :disabled="dirty || !status?.active || disconnected"
                  @click="probe(p.profile.name)"
                  >探测</el-button
                >
              </div>
            </template>
            <span v-else class="pool-meta pool-meta-wide">直连</span>
          </li>
        </ul>
      </section>

      <section class="card">
        <header class="card-header">
          <h2>服务</h2>
        </header>
        <div class="button-row">
          <el-button
            type="primary"
            :disabled="status?.active || conflict || disconnected"
            @click="service('start')"
            >启动服务</el-button
          ><el-button
            :disabled="!status?.active || conflict || disconnected"
            @click="service('stop')"
            >停止服务</el-button
          ><el-button :disabled="conflict || disconnected" @click="service('restart')"
            >重启服务</el-button
          >
        </div>
        <a v-if="status?.rollbackAvailable" class="rollback-link" href="#/operations"
          >可回滚至 GOST {{ status.rollbackTarget?.version }} ·
          {{ status.rollbackTarget?.activeProfile }} →</a
        >
      </section>
    </div>
  </div>
</template>
