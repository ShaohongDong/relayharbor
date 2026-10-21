<script setup lang="ts">
import { computed } from 'vue'
import Icon from '../Icon.vue'
import { useConsole } from '../console'

const {
  status,
  disconnected,
  conflict,
  dirty,
  base,
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
const activeProfile = computed(() =>
  base.value?.profiles.find((p) => p.name === status.value?.activeProfile),
)
</script>

<template>
  <div class="page-stack">
    <section class="stat-grid" aria-label="运行概况">
      <article class="stat" :class="`tone-${serviceTone}`">
        <span class="stat-icon"><Icon name="activity" /></span>
        <div>
          <span class="stat-label">转发服务</span>
          <strong class="stat-value">{{ serviceLabel }}</strong>
          <span class="stat-sub">{{ status?.enabled ? '已开启开机启动' : '未开启开机启动' }}</span>
        </div>
      </article>
      <article class="stat">
        <span class="stat-icon"><Icon name="exit" /></span>
        <div>
          <span class="stat-label">当前出口</span>
          <strong class="stat-value">{{ status?.activeProfile ?? '—' }}</strong>
          <span class="stat-sub">{{
            activeProfile?.type === 'direct'
              ? '直连，无上游节点'
              : `代理池 · ${activeProfile?.nodes?.length ?? 0} 个节点`
          }}</span>
        </div>
      </article>
      <article class="stat">
        <span class="stat-icon"><Icon name="link" /></span>
        <div>
          <span class="stat-label">活动连接</span>
          <strong class="stat-value" data-testid="stat-connections">{{
            live ? (status?.connections ?? 0) : '—'
          }}</strong>
          <span class="stat-sub">每 5 秒自动刷新</span>
        </div>
      </article>
      <article class="stat">
        <span class="stat-icon"><Icon name="shield" /></span>
        <div>
          <span class="stat-label">健康节点</span>
          <strong class="stat-value" data-testid="stat-health"
            >{{ live ? totals.healthy : '—' }}<small> / {{ totals.total }}</small></strong
          >
          <span class="stat-sub"
            >{{ poolHealth.filter((p) => p.profile.type === 'pool').length }} 个代理池</span
          >
        </div>
      </article>
    </section>

    <div class="overview-grid">
      <section class="card">
        <header class="card-header">
          <div>
            <h2>代理池与出口</h2>
            <p>已应用配置中的出口及节点健康情况</p>
          </div>
          <el-button @click="go('nodes')">管理节点</el-button>
        </header>
        <ul class="pool-list">
          <li v-for="p in poolHealth" :key="p.profile.name" class="pool-item">
            <div class="pool-main">
              <div class="pool-name">
                <strong>{{ p.profile.name }}</strong>
                <span v-if="p.profile.name === status?.activeProfile" class="badge badge-primary"
                  >当前出口</span
                >
                <span class="badge">{{ p.profile.type === 'direct' ? '直连' : '代理池' }}</span>
              </div>
              <template v-if="p.profile.type === 'pool'">
                <div
                  class="health-bar"
                  role="img"
                  :aria-label="`${p.profile.name} 健康 ${p.healthy} / ${p.total}`"
                >
                  <span
                    class="health-ok"
                    :style="{ width: live && p.total ? (p.healthy / p.total) * 100 + '%' : '0' }"
                  /><span
                    class="health-bad"
                    :style="{ width: live && p.total ? (p.failed / p.total) * 100 + '%' : '0' }"
                  />
                </div>
                <span class="pool-meta"
                  >{{ live ? `${p.healthy} 正常 · ${p.failed} 故障 · ` : '' }}{{ p.total }} 个节点 ·
                  {{ p.profile.probe?.type?.toUpperCase() ?? '未设置' }} 探测</span
                >
              </template>
              <span v-else class="pool-meta">直接连接目标，不经过上游节点；支持 UDP</span>
            </div>
            <div v-if="p.profile.type === 'pool'" class="pool-actions">
              <el-button link type="primary" @click="showPool(p.profile.name)">查看节点</el-button
              ><el-button
                link
                :disabled="dirty || !status?.active || disconnected"
                @click="probe(p.profile.name)"
                >探测</el-button
              >
            </div>
          </li>
        </ul>
      </section>

      <section class="card">
        <header class="card-header">
          <div>
            <h2>服务控制</h2>
            <p>{{ status?.arch }} · GOST {{ status?.version }}</p>
          </div>
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
        <div class="callout">
          <strong>回滚</strong>
          <p v-if="status?.rollbackAvailable">
            可恢复到 GOST {{ status.rollbackTarget?.version }} · 出口
            {{ status.rollbackTarget?.activeProfile }}。
            <a href="#/operations">前往服务运维</a>
          </p>
          <p v-else>暂无可恢复的上一份配置。</p>
        </div>
        <p class="hint">切换出口为热更新，已有连接保持原路径；全池不可用时新连接返回失败。</p>
      </section>
    </div>
  </div>
</template>
