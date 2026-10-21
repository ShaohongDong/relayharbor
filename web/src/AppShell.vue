<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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
  go,
  showError,
  logout,
  refresh,
  reload,
  apply,
  useProfile,
} = useConsole()

const pages: { view: View; label: string; icon: IconName; subtitle: string }[] = [
  { view: 'overview', label: '概览', icon: 'overview', subtitle: '服务状态、出口与节点健康' },
  {
    view: 'nodes',
    label: '代理池与节点',
    icon: 'nodes',
    subtitle: '健康节点按新连接轮询；故障切换限于当前池',
  },
  {
    view: 'listener',
    label: '入口与认证',
    icon: 'listener',
    subtitle: 'SOCKS5 入口地址与必需的入口认证',
  },
  {
    view: 'subscriptions',
    label: '客户端订阅',
    icon: 'subscriptions',
    subtitle: 'AnyTLS 用户、独立端口与订阅链接',
  },
  { view: 'https', label: 'HTTPS 证书', icon: 'https', subtitle: '域名证书、自动续期与 Nginx' },
  {
    view: 'operations',
    label: '服务运维',
    icon: 'operations',
    subtitle: '启动、停止、回滚与管理日志',
  },
  { view: 'setup', label: '部署设置', icon: 'setup', subtitle: '域名 HTTPS、AnyTLS 与基础模式' },
]
const current = computed(() => pages.find((p) => p.view === view.value) ?? pages[0])
const navOpen = ref(false)
watch(view, () => (navOpen.value = false))
</script>

<template>
  <div class="shell" :class="{ 'nav-open': navOpen, 'has-savebar': dirty || conflict }">
    <aside class="sidebar" aria-label="侧边栏">
      <div class="sidebar-brand">
        <span class="brand-mark">R</span>
        <div>
          <strong>RelayHarbor</strong>
          <span>GOST 控制台</span>
        </div>
      </div>
      <nav class="sidebar-nav" aria-label="主导航">
        <a
          v-for="p in pages"
          :key="p.view"
          :href="`#/${p.view}`"
          :aria-current="view === p.view ? 'page' : undefined"
          @click="navOpen = false"
          ><Icon :name="p.icon" /><span>{{ p.label }}</span></a
        >
      </nav>
      <div class="sidebar-foot">
        <span class="mono">GOST {{ status?.version }} · {{ status?.arch }}</span>
        <button type="button" class="sidebar-logout" :disabled="busy || refreshing" @click="logout">
          <Icon name="logout" :size="16" /><span>断开认证</span>
        </button>
      </div>
    </aside>
    <div class="scrim" @click="navOpen = false" />

    <div class="main">
      <header class="topbar">
        <button
          type="button"
          class="menu-button"
          aria-label="打开导航"
          :aria-expanded="navOpen"
          @click="navOpen = !navOpen"
        >
          <Icon name="menu" />
        </button>
        <div class="page-title">
          <h1>{{ current.label }}</h1>
          <p>{{ current.subtitle }}</p>
        </div>
        <div class="topbar-actions">
          <span class="status-pill" :class="`tone-${serviceTone}`" role="status"
            ><span class="status-dot" />{{ serviceLabel }}</span
          >
          <div class="exit-picker">
            <span>当前出口</span
            ><el-select
              :model-value="status?.activeProfile"
              aria-label="当前出口"
              :disabled="busy || refreshing || conflict || disconnected"
              @change="useProfile"
              ><el-option
                v-for="p in base!.profiles"
                :key="p.name"
                :label="p.name + (p.type === 'direct' ? ' · 直连' : '')"
                :value="p.name"
            /></el-select>
          </div>
          <span class="conn-meta" title="当前连接数"
            ><Icon name="link" :size="15" />{{ status?.connections ?? 0 }}</span
          >
          <el-button :disabled="busy || refreshing" :loading="refreshing" @click="refresh"
            ><Icon v-if="!refreshing" name="refresh" :size="15" /><span class="btn-text"
              >刷新状态</span
            ></el-button
          >
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
          v-else-if="view === 'setup'"
          :token="token"
          @done="go('overview')"
          @error="showError"
        />
      </main>

      <transition name="slide-up">
        <footer v-if="dirty || conflict" class="savebar" role="region" aria-label="未应用修改">
          <div class="savebar-text">
            <strong>有未应用修改</strong
            ><span>{{
              restart ? '应用时需要重启服务，已有连接会中断' : '应用时保留已有连接'
            }}</span>
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
