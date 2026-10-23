<script setup lang="ts">
import { computed } from 'vue'
import Icon, { type IconName } from './Icon.vue'
import { useConsole, type View } from './console'
import OverviewView from './views/OverviewView.vue'
import NodesView from './views/NodesView.vue'
import ListenerView from './views/ListenerView.vue'
import OperationsView from './views/OperationsView.vue'
import ProfilesDrawer from './views/ProfilesDrawer.vue'
import ProfileEditor from './views/ProfileEditor.vue'
import NodeEditor from './views/NodeEditor.vue'
import PreviewDrawer from './views/PreviewDrawer.vue'
import TLSPanel from './TLSPanel.vue'
import AnyTLSPanel from './AnyTLSPanel.vue'
import SetupPanel from './SetupPanel.vue'

const {
  token,
  busy,
  refreshing,
  view,
  setupDone,
  error,
  disconnected,
  conflict,
  base,
  status,
  revision,
  dirty,
  restart,
  previewOpen,
  serviceLabel,
  serviceTone,
  setupFinished,
  showError,
  logout,
  refresh,
  reload,
  apply,
  useProfile,
} = useConsole()

const consolePages: { view: View; label: string; icon: IconName }[] = [
  { view: 'overview', label: '概览', icon: 'overview' },
  { view: 'nodes', label: '代理池与节点', icon: 'nodes' },
  { view: 'listener', label: '入口与认证', icon: 'listener' },
  { view: 'subscriptions', label: '客户端订阅', icon: 'subscriptions' },
  { view: 'https', label: 'HTTPS 证书', icon: 'https' },
  { view: 'operations', label: '服务运维', icon: 'operations' },
]
const setupPage = { view: 'setup' as View, label: '部署设置', icon: 'setup' as IconName }
// Before completion only the wizard is reachable; afterwards it has no entry at all.
const pages = computed(() => (setupDone.value === false ? [setupPage] : consolePages))
const current = computed(() => pages.value.find((p) => p.view === view.value) ?? pages.value[0])
</script>

<template>
  <div class="shell" :class="{ 'has-savebar': dirty || conflict }">
    <aside class="rail" aria-label="侧边栏">
      <span class="brand-mark" title="RelayHarbor">R</span>
      <nav class="rail-nav" aria-label="主导航">
        <a
          v-for="p in pages"
          :key="p.view"
          class="rail-item"
          :href="`#/${p.view}`"
          :aria-label="p.label"
          :data-tip="p.label"
          :aria-current="view === p.view ? 'page' : undefined"
          ><Icon :name="p.icon" :size="19"
        /></a>
      </nav>
      <button
        type="button"
        class="rail-item"
        aria-label="断开认证"
        :data-tip="`断开认证 · GOST ${status?.version ?? ''} · ${status?.arch ?? ''}`"
        :disabled="busy || refreshing"
        @click="logout"
      >
        <Icon name="logout" :size="18" />
      </button>
    </aside>

    <div class="main">
      <header class="topbar">
        <h1>{{ current.label }}</h1>
        <div v-if="setupDone" class="topbar-actions">
          <span
            class="status-pill"
            :class="`tone-${serviceTone}`"
            role="status"
            :title="serviceLabel"
            ><span class="status-dot" /><span class="status-text">{{ serviceLabel }}</span></span
          >
          <el-select
            :model-value="status?.activeProfile"
            aria-label="当前出口"
            :disabled="busy || refreshing || conflict || disconnected"
            @change="useProfile"
            ><template #prefix><Icon name="exit" :size="14" /></template
            ><el-option
              v-for="p in base!.profiles"
              :key="p.name"
              :label="p.name + (p.type === 'direct' ? ' · 直连' : '')"
              :value="p.name"
          /></el-select>
          <span class="conn-meta" title="活动连接"
            ><Icon name="link" :size="15" />{{ status?.connections ?? 0 }}</span
          >
          <button
            type="button"
            class="icon-button"
            aria-label="刷新状态"
            title="刷新状态"
            :disabled="busy || refreshing"
            @click="refresh"
          >
            <Icon name="refresh" :size="15" :class="{ spin: refreshing }" />
          </button>
        </div>
      </header>

      <main class="content" v-loading="busy" element-loading-text="操作进行中，请等待确认…">
        <el-alert
          v-if="error"
          :title="error"
          type="error"
          :closable="true"
          show-icon
          @close="error = ''"
        />
        <el-alert
          v-if="conflict"
          title="服务端配置已变化；当前草稿保留，重新加载后才能继续提交。"
          type="warning"
          :closable="false"
          show-icon
          ><el-button size="small" @click="reload" :disabled="busy || refreshing"
            >重新加载配置</el-button
          ></el-alert
        >
        <OverviewView v-if="view === 'overview'" />
        <NodesView v-else-if="view === 'nodes'" />
        <ListenerView v-else-if="view === 'listener'" />
        <AnyTLSPanel v-else-if="view === 'subscriptions'" :token="token" @error="showError" />
        <TLSPanel
          v-else-if="view === 'https'"
          :key="token"
          :token="token"
          :revision="revision"
          :blocked="conflict || disconnected || busy"
          @error="showError"
        />
        <OperationsView v-else-if="view === 'operations'" />
        <SetupPanel
          v-else-if="view === 'setup' && setupDone === false"
          :token="token"
          @done="setupFinished"
          @error="showError"
        />
      </main>

      <transition name="slide-up">
        <footer v-if="dirty || conflict" class="savebar" role="region" aria-label="未应用修改">
          <div class="savebar-text">
            <strong>有未应用修改</strong
            ><span v-if="restart" class="savebar-warn">应用时需要重启服务，已有连接会中断</span>
          </div>
          <div class="savebar-actions">
            <el-button :disabled="!dirty || busy || refreshing" @click="reload">放弃修改</el-button
            ><el-button @click="previewOpen = true">预览变更</el-button
            ><el-button
              type="primary"
              :disabled="!dirty || busy || refreshing || conflict || disconnected"
              @click="apply"
              >校验并应用</el-button
            >
          </div>
        </footer>
      </transition>
    </div>

    <ProfilesDrawer />
    <ProfileEditor />
    <NodeEditor />
    <PreviewDrawer />
  </div>
</template>
