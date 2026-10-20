<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { APIError, request } from './api'
import TLSPanel from './TLSPanel.vue'
import AnyTLSPanel from './AnyTLSPanel.vue'
import {
  clone,
  needsRestart,
  payload,
  redacted,
  validateDraft,
  type Config,
  type Node,
  type Profile,
  type Status,
  type LogResult,
} from './model'

const token = ref(''),
  tokenInput = ref(''),
  busy = ref(false),
  refreshing = ref(false)
const error = ref(''),
  disconnected = ref(false),
  conflict = ref(false)
const base = ref<Config>(),
  draft = ref<Config>(),
  status = ref<Status>(),
  revision = ref('')
const tab = ref('nodes'),
  search = ref(''),
  filterPool = ref(''),
  filterHealth = ref(''),
  page = ref(1)
const sortKey = ref(''),
  sortOrder = ref('')
const profilesOpen = ref(false),
  nodeOpen = ref(false),
  profileOpen = ref(false)
const nodeEditor = ref<Node>({ id: '', type: 'socks5', addr: '' }),
  nodePool = ref(''),
  nodeOrigin = ref<{ pool: string; id: string }>()
const profileEditor = ref<Profile>({ name: '', type: 'direct' }),
  profileOrigin = ref('')
const logs = ref<LogResult>(),
  logLimit = ref(100)
const dirty = computed(
  () => !!draft.value && !!base.value && JSON.stringify(draft.value) !== JSON.stringify(base.value),
)
const restart = computed(
  () => !!draft.value && !!base.value && needsRestart(draft.value, base.value),
)
const pools = computed(() => draft.value?.profiles.filter((p) => p.type === 'pool') ?? [])
const preview = computed(() =>
  draft.value ? JSON.stringify(redacted(payload(draft.value)), null, 2) : '',
)
const changeSummary = computed(() => {
  if (!draft.value || !base.value) return ''
  const count = (c: Config) => c.profiles.reduce((sum, p) => sum + (p.nodes?.length ?? 0), 0)
  return `代理池 / 直连 ${base.value.profiles.length} → ${draft.value.profiles.length} · 节点 ${count(base.value)} → ${count(draft.value)} · 应用后出口 ${draft.value.activeProfile}`
})
const oldNodes = computed(
  () =>
    new Map(
      base.value?.profiles.flatMap((p) =>
        (p.nodes ?? []).map((n) => [`${p.name}/${n.id}`, n] as const),
      ),
    ),
)
const nodeStates = computed(
  () => new Map(status.value?.nodes?.map((n) => [`${n.profile}/${n.id}`, n] as const)),
)
const rows = computed(() =>
  (
    draft.value?.profiles.flatMap((p) =>
      (p.nodes ?? []).map((n) => {
        const old = oldNodes.value.get(`${p.name}/${n.id}`)
        const state =
          old && JSON.stringify(old) === JSON.stringify(n)
            ? nodeStates.value.get(`${p.name}/${n.id}`)
            : undefined
        return {
          ...n,
          profile: p.name,
          state,
          health: disconnected.value
            ? '连接中断'
            : !status.value?.active
              ? '服务停止'
              : state?.healthy === true
                ? '正常'
                : state?.healthy === false
                  ? '故障'
                  : '未知',
        }
      }),
    ) ?? []
  )
    .filter(
      (n) =>
        (!filterPool.value || n.profile === filterPool.value) &&
        (!filterHealth.value || n.health === filterHealth.value) &&
        `${n.profile} ${n.id} ${n.type} ${n.addr}`
          .toLowerCase()
          .includes(search.value.toLowerCase()),
    )
    .sort((a, b) => {
      if (!sortOrder.value) return 0
      const av = sortKey.value === 'profile' ? a.profile : a.id,
        bv = sortKey.value === 'profile' ? b.profile : b.id
      return av.localeCompare(bv) * (sortOrder.value === 'ascending' ? 1 : -1)
    }),
)
function sortChanged(s: { prop: string; order: string }) {
  sortKey.value = s.prop
  sortOrder.value = s.order
  page.value = 1
}
function rowKey(row: { profile: string; id: string }) {
  return `${row.profile}/${row.id}`
}
const visibleRows = computed(() => rows.value.slice((page.value - 1) * 50, page.value * 50))
const serviceLabel = computed(() =>
  disconnected.value
    ? '连接中断'
    : !status.value?.active
      ? '服务已停止'
      : status.value.ready
        ? status.value.available
          ? '运行中'
          : '当前出口不可用'
        : '正在启动 / 未就绪',
)
function disconnect() {
  token.value = ''
  tokenInput.value = ''
  base.value = undefined
  draft.value = undefined
  status.value = undefined
  revision.value = ''
  logs.value = undefined
  nodeEditor.value = { id: '', type: 'socks5', addr: '' }
  profileEditor.value = { name: '', type: 'direct' }
  nodeOpen.value = false
  profilesOpen.value = false
  profileOpen.value = false
  conflict.value = false
  disconnected.value = false
}
function showError(e: unknown) {
  error.value = e instanceof Error ? e.message : '操作失败'
  if (e instanceof APIError) {
    if (e.status === 401) disconnect()
    if (e.code === 'revision_conflict') {
      conflict.value = true
      error.value = '配置已被其他客户端修改。草稿已保留，请重新加载后编辑。'
    }
  } else disconnected.value = true
}
async function load() {
  // Do not combine responses from different configuration revisions.
  for (let i = 0; i < 3; i++) {
    const c = await request<Config>(token.value, '/config')
    const s = await request<Status>(token.value, '/status')
    if (c.revision !== s.revision) continue
    base.value = c.data
    draft.value = clone(c.data)
    status.value = s.data
    revision.value = c.revision
    conflict.value = false
    disconnected.value = false
    page.value = 1
    return
  }
  throw new Error('配置持续变化，请稍后重新加载')
}
async function action(fn: () => Promise<void>, mutation = false) {
  if (busy.value || refreshing.value) return
  busy.value = true
  error.value = ''
  try {
    await fn()
  } catch (e) {
    showError(e)
    if (mutation && !(e instanceof APIError)) {
      error.value = '连接异常，操作结果待确认。请刷新状态确认后再操作。'
      // Only read back; never replay a mutation after a transport failure.
      try {
        const s = await request<Status>(token.value, '/status')
        status.value = s.data
        disconnected.value = false
        if (s.revision !== revision.value) conflict.value = true
      } catch {
        /* retain the original error */
      }
    }
  } finally {
    busy.value = false
  }
}
async function login() {
  await action(async () => {
    token.value = tokenInput.value.trim()
    tokenInput.value = ''
    try {
      await load()
    } catch (e) {
      disconnect()
      throw e
    }
  })
}
async function refresh() {
  if (!token.value || busy.value || refreshing.value) return
  refreshing.value = true
  try {
    const s = await request<Status>(token.value, '/status')
    status.value = s.data
    disconnected.value = false
    if (s.revision !== revision.value) {
      if (dirty.value) conflict.value = true
      else await load()
    }
  } catch (e) {
    showError(e)
  } finally {
    refreshing.value = false
  }
}
async function confirm(message: string, title = '确认操作') {
  try {
    await ElMessageBox.confirm(message, title, {
      confirmButtonText: '确认',
      cancelButtonText: '返回',
      type: 'warning',
      closeOnClickModal: false,
    })
    return true
  } catch {
    return false
  }
}
async function ensureClean() {
  if (!dirty.value) return true
  if (
    !(await confirm(
      '存在未应用的配置修改。放弃草稿后才能继续；也可以返回页面先应用配置。',
      '未应用修改',
    ))
  )
    return false
  draft.value = clone(base.value!)
  return true
}
async function reload() {
  if (await ensureClean()) await action(load)
}
async function apply() {
  await action(async () => {
    const problem = validateDraft(draft.value!, base.value!)
    if (problem) throw new APIError(400, 'invalid_draft', problem)
    const candidate = payload(draft.value!)
    await request(token.value, '/config/validate', 'POST', candidate)
    if (
      restart.value &&
      !(await confirm(
        '入口地址、账号或 UDP 设置发生变化，需要重启转发服务；已有连接会中断。是否应用？',
      ))
    )
      return
    await request(token.value, '/config', 'PUT', candidate, revision.value)
    // Immediately forget newly entered passwords after acknowledged success.
    draft.value = clone(redacted(draft.value!))
    base.value = clone(draft.value)
    await load()
    ElMessage.success('配置已应用')
  }, true)
}
async function useProfile(name: string) {
  if (name === status.value?.activeProfile || !(await ensureClean())) return
  await action(async () => {
    await request(token.value, '/active-profile', 'PUT', { name }, revision.value)
    await load()
    ElMessage.success('出口已切换，已有连接继续使用原路径')
  }, true)
}
async function probe(name: string) {
  if (!(await ensureClean())) return
  await action(async () => {
    await request(token.value, `/profiles/${encodeURIComponent(name)}/probe`, 'POST', {})
    const s = await request<Status>(token.value, '/status')
    status.value = s.data
    ElMessage.success('探测完成，故障节点仍遵循冷却时间')
  })
}
async function service(verb: 'start' | 'stop' | 'restart' | 'rollback') {
  if (!(await ensureClean())) return
  const labels = { start: '启动', stop: '停止', restart: '重启', rollback: '回滚' }
  const target = status.value?.rollbackTarget
  const message =
    verb === 'rollback'
      ? `恢复上一份配置与 GOST ${target?.version ?? ''}，出口 ${target?.activeProfile ?? ''}，${target?.active ? '运行' : '停止'} / ${target?.enabled ? '开机启动' : '不开机启动'}。可能重启服务并中断已有连接。`
      : `${labels[verb]}转发服务。${verb === 'start' ? '等待入口就绪后返回。' : '已有连接会中断。'}管理页面仍可继续使用。`
  if (verb !== 'start' && !(await confirm(message, `${labels[verb]}服务`))) return
  await action(async () => {
    await request(
      token.value,
      verb === 'rollback' ? '/rollback' : `/service/${verb}`,
      'POST',
      {},
      revision.value,
    )
    await load()
    ElMessage.success(`${labels[verb]}完成`)
  }, true)
}
async function readLogs() {
  await action(async () => {
    logs.value = (await request<LogResult>(token.value, `/logs?limit=${logLimit.value}`)).data
  })
}
function editNode(pool?: string, node?: Node) {
  nodeOrigin.value = node && pool ? { pool, id: node.id } : undefined
  nodePool.value = pool ?? pools.value[0]?.name ?? ''
  nodeEditor.value = node
    ? clone({
        id: node.id,
        type: node.type,
        addr: node.addr,
        ...(node.auth !== undefined ? { auth: node.auth } : {}),
      })
    : { id: '', type: 'socks5', addr: '' }
  nodeOpen.value = true
}
function saveNode() {
  const p = draft.value!.profiles.find((v) => v.name === nodePool.value && v.type === 'pool')
  if (!p || !nodeEditor.value.id || !nodeEditor.value.addr) {
    ElMessage.error('请选择代理池并填写节点 ID、地址')
    return
  }
  if (
    p.nodes?.some(
      (n) =>
        n.id === nodeEditor.value.id &&
        !(nodeOrigin.value?.pool === p.name && nodeOrigin.value.id === n.id),
    )
  ) {
    ElMessage.error('池内节点 ID 已存在')
    return
  }
  if (nodeOrigin.value) {
    const old = draft.value!.profiles.find((v) => v.name === nodeOrigin.value!.pool)
    if (old?.nodes) old.nodes = old.nodes.filter((n) => n.id !== nodeOrigin.value!.id)
  }
  p.nodes ??= []
  p.nodes.push(clone(nodeEditor.value))
  nodeOpen.value = false
  page.value = 1
  nodeEditor.value = { id: '', type: 'socks5', addr: '' }
}
async function removeNode(pool: string, id: string) {
  if (!(await confirm(`从草稿中删除 ${pool}/${id}？应用配置后生效。`))) return
  const p = draft.value!.profiles.find((p) => p.name === pool)!
  p.nodes = p.nodes?.filter((n) => n.id !== id)
  page.value = 1
}
function editProfile(p?: Profile) {
  profileOrigin.value = p?.name ?? ''
  profileEditor.value = p
    ? clone(p)
    : {
        name: '',
        type: 'pool',
        nodes: [],
        probe: { type: 'tcp', addr: '', interval: 30, timeout: 5 },
      }
  profileOpen.value = true
}
function profileTypeChanged() {
  if (profileEditor.value.type === 'pool') {
    profileEditor.value.nodes ??= []
    profileEditor.value.probe ??= { type: 'tcp', addr: '', interval: 30, timeout: 5 }
  }
}
async function saveProfile() {
  const p = clone(profileEditor.value)
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(p.name)) {
    ElMessage.error('请填写有效 profile 名称')
    return
  }
  if (draft.value!.profiles.some((v) => v.name === p.name && v.name !== profileOrigin.value)) {
    ElMessage.error('profile 名称已存在')
    return
  }
  if (p.type === 'direct') {
    if (p.nodes?.length && !(await confirm('改为直连将移除草稿中的池节点与探测设置。'))) return
    delete p.nodes
    delete p.probe
  }
  const i = draft.value!.profiles.findIndex((v) => v.name === profileOrigin.value)
  if (i >= 0) draft.value!.profiles.splice(i, 1, p)
  else draft.value!.profiles.push(p)
  if (draft.value!.activeProfile === profileOrigin.value) draft.value!.activeProfile = p.name
  profileOpen.value = false
  page.value = 1
}
async function removeProfile(name: string) {
  if (draft.value!.activeProfile === name) {
    ElMessage.error('请先选择另一个“应用后出口”，再删除此 profile')
    return
  }
  if (!(await confirm(`从草稿中删除 ${name} 及其所有节点？`))) return
  draft.value!.profiles = draft.value!.profiles.filter((p) => p.name !== name)
  if (filterPool.value === name) filterPool.value = ''
  page.value = 1
}
function setNodeAuth(enabled: string | number | boolean) {
  nodeEditor.value.auth = enabled ? { username: '', password: '' } : null
}
function changedFilter() {
  page.value = 1
}
function beforeUnload(e: BeforeUnloadEvent) {
  if (dirty.value || busy.value) {
    e.preventDefault()
    e.returnValue = ''
  }
}
let timer: ReturnType<typeof setInterval>
onMounted(() => {
  timer = setInterval(() => {
    if (!document.hidden) void refresh()
  }, 5000)
  window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  window.removeEventListener('beforeunload', beforeUnload)
})
</script>

<template>
  <div v-if="!base" class="login-shell">
    <main class="login-card">
      <div class="brand-mark">G</div>
      <h1>GOST 控制台</h1>
      <p class="muted">代理配置与运行管理</p>
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
      <el-form @submit.prevent="login" label-position="top">
        <el-form-item label="管理 token"
          ><el-input
            v-model="tokenInput"
            aria-label="管理 token"
            type="password"
            autocomplete="off"
            placeholder="输入本机管理 token"
        /></el-form-item>
        <el-button
          native-type="submit"
          type="primary"
          size="large"
          :loading="busy"
          :disabled="!tokenInput.trim()"
          class="full"
          >连接控制台</el-button
        >
      </el-form>
      <p class="hint">仅用于本机或 SSH 隧道访问。Token 只在当前页面内存中保存。</p>
      <p class="hint">在服务器读取：<code>sudo cat /var/lib/gost-deploy/api-token</code></p>
    </main>
  </div>
  <div v-else class="console" v-loading="busy" element-loading-text="操作进行中，请等待确认…">
    <header class="topbar">
      <div class="brand"><span class="brand-mark small">G</span><strong>GOST 控制台</strong></div>
      <el-tag :type="disconnected || !status?.available ? 'warning' : 'success'">{{
        serviceLabel
      }}</el-tag>
      <div class="exit-picker">
        <label>当前出口</label
        ><el-select
          :model-value="status?.activeProfile"
          aria-label="当前出口"
          :disabled="busy || refreshing || conflict || disconnected"
          @change="useProfile"
          ><el-option
            v-for="p in base.profiles"
            :key="p.name"
            :label="p.name + (p.type === 'direct' ? ' · 直连' : '')"
            :value="p.name"
        /></el-select>
      </div>
      <span class="top-meta"
        >GOST {{ status?.version }} · {{ status?.connections ?? 0 }} 个连接</span
      >
      <el-button :disabled="busy || refreshing" @click="refresh">刷新状态</el-button>
      <el-button :disabled="busy || refreshing" @click="disconnect">断开认证</el-button>
    </header>
    <main class="content">
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
        ><el-button @click="reload" :disabled="busy || refreshing"
          >重新加载配置</el-button
        ></el-alert
      >
      <el-tabs v-model="tab" class="main-tabs">
        <el-tab-pane label="代理池与节点" name="nodes">
          <div class="toolbar">
            <el-input
              v-model="search"
              aria-label="搜索节点"
              placeholder="搜索节点 ID、地址、协议…"
              clearable
              class="search"
              @input="changedFilter"
            />
            <el-select
              v-model="filterPool"
              aria-label="代理池筛选"
              placeholder="全部代理池"
              clearable
              @change="changedFilter"
              ><el-option
                v-for="p in draft!.profiles"
                :key="p.name"
                :label="p.name"
                :value="p.name"
            /></el-select>
            <el-select
              v-model="filterHealth"
              aria-label="健康状态筛选"
              placeholder="全部健康状态"
              clearable
              @change="changedFilter"
              ><el-option
                v-for="h in ['正常', '故障', '未知', '服务停止', '连接中断']"
                :key="h"
                :label="h"
                :value="h"
            /></el-select>
            <span class="spacer" /><el-button @click="profilesOpen = true">管理代理池</el-button
            ><el-button type="primary" :disabled="!pools.length" @click="editNode()"
              >添加节点</el-button
            >
          </div>
          <div class="pool-strip">
            <span class="muted"
              >{{ draft!.profiles.length }} 个 profile · {{ rows.length }} 个节点</span
            ><span
              v-for="p in draft!.profiles.filter((p) => p.type === 'direct')"
              :key="p.name"
              class="direct-chip"
              >{{ p.name }} · 直连，无上游节点</span
            ><el-button
              v-if="filterPool && pools.some((p) => p.name === filterPool)"
              link
              type="primary"
              :disabled="!status?.active || dirty || disconnected"
              @click="probe(filterPool)"
              >探测所选池</el-button
            >
          </div>
          <el-table
            :data="visibleRows"
            stripe
            :row-key="rowKey"
            @sort-change="sortChanged"
            empty-text="没有匹配的节点。可先创建代理池，再添加节点。"
            class="node-table"
          >
            <el-table-column prop="profile" label="所属池" width="140" sortable="custom" />
            <el-table-column prop="id" label="节点 ID" min-width="140" sortable="custom" />
            <el-table-column prop="type" label="类型" width="100"
              ><template #default="{ row }"
                ><span class="protocol">{{
                  row.type === 'http' ? 'HTTP' : 'SOCKS5'
                }}</span></template
              ></el-table-column
            >
            <el-table-column prop="addr" label="地址" min-width="190" show-overflow-tooltip />
            <el-table-column prop="health" label="健康" width="110"
              ><template #default="{ row }"
                ><el-tag
                  :type="
                    row.health === '正常' ? 'success' : row.health === '故障' ? 'danger' : 'info'
                  "
                  effect="plain"
                  >{{ row.health }}</el-tag
                ></template
              ></el-table-column
            >
            <el-table-column label="延迟" width="100"
              ><template #default="{ row }">{{
                row.state?.latencyMs == null || disconnected || !status?.active
                  ? '—'
                  : row.state.latencyMs.toFixed(1) + ' ms'
              }}</template></el-table-column
            >
            <el-table-column label="最近探测" min-width="150"
              ><template #default="{ row }">{{
                row.state?.lastProbe
                  ? new Date(row.state.lastProbe * 1000).toLocaleTimeString('zh-CN', {
                      hour12: false,
                    })
                  : '—'
              }}</template></el-table-column
            >
            <el-table-column label="失败次数" width="90"
              ><template #default="{ row }">{{
                row.state?.failures ?? '—'
              }}</template></el-table-column
            >
            <el-table-column label="操作" width="150" fixed="right"
              ><template #default="{ row }"
                ><el-button link type="primary" @click="editNode(row.profile, row)">编辑</el-button
                ><el-button link type="danger" @click="removeNode(row.profile, row.id)"
                  >删除</el-button
                ></template
              ></el-table-column
            >
          </el-table>
          <el-pagination
            v-if="rows.length > 50"
            v-model:current-page="page"
            :page-size="50"
            :total="rows.length"
            layout="total, prev, pager, next"
            class="pagination"
          />
          <p class="hint">
            健康节点按新连接轮询；故障切换限于当前池。已有连接保持原路径，全池不可用时返回失败。
          </p>
        </el-tab-pane>
        <el-tab-pane label="入口与认证" name="listener">
          <section class="settings">
            <h2>SOCKS5 入口</h2>
            <p class="muted">入口认证始终必需。此处修改应用后需要重启转发服务。</p>
            <el-form label-position="top"
              ><el-form-item label="监听地址"
                ><el-input
                  v-model="draft!.listener.addr"
                  aria-label="监听地址"
                  placeholder="0.0.0.0:1080" /></el-form-item
              ><el-form-item label="UDP 转发"
                ><el-switch v-model="draft!.listener.udp" aria-label="UDP 转发" /><span
                  class="hint inline"
                  >仅直连 profile 支持 UDP</span
                ></el-form-item
              ></el-form
            >
            <h2>入口用户</h2>
            <div v-for="(u, i) in draft!.listener.users" :key="i" class="user-row">
              <el-input
                v-model="u.username"
                :aria-label="'入口用户名 ' + (i + 1)"
                placeholder="用户名"
                autocomplete="off"
              /><el-input
                v-model="u.password"
                :aria-label="'入口密码 ' + (i + 1)"
                type="password"
                autocomplete="new-password"
                :placeholder="u.passwordSet ? '已设置，留空保留原密码' : '必须填写密码'"
              /><el-button
                type="danger"
                plain
                :disabled="draft!.listener.users.length <= 1"
                @click="draft!.listener.users.splice(i, 1)"
                >移除</el-button
              >
            </div>
            <el-button @click="draft!.listener.users.push({ username: '', password: '' })"
              >添加入口用户</el-button
            >
          </section>
        </el-tab-pane>
        <el-tab-pane label="配置预览" name="preview"
          ><div class="section-heading">
            <div>
              <h2>候选配置</h2>
              <p class="muted">{{ changeSummary }}</p>
            </div>
            <el-tag :type="restart ? 'warning' : 'success'">{{
              restart ? '应用时需要重启' : '可热更新'
            }}</el-tag>
          </div>
          <p class="hint">
            仅显示脱敏预览；密码不会出现在此处。校验为静态检查，实际应用仍可能因运行环境失败。
          </p>
          <pre class="config-preview" data-testid="config-preview">{{ preview }}</pre>
        </el-tab-pane>
        <el-tab-pane label="客户端订阅" name="subscriptions" lazy>
          <AnyTLSPanel v-if="tab === 'subscriptions'" :token="token" @error="showError" />
        </el-tab-pane>
        <el-tab-pane label="服务运维" name="operations">
          <TLSPanel
            v-if="tab === 'operations'"
            :key="token"
            :token="token"
            :revision="revision"
            :blocked="conflict || disconnected || busy"
            @error="showError"
          />
          <section class="operations">
            <h2>转发服务</h2>
            <p class="muted">
              {{ serviceLabel }} · {{ status?.enabled ? '已开启开机启动' : '未开启开机启动' }} ·
              {{ status?.arch }}
            </p>
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
            <h2>回滚</h2>
            <p class="muted" v-if="status?.rollbackAvailable">
              目标 GOST {{ status.rollbackTarget?.version }} · 出口
              {{ status.rollbackTarget?.activeProfile }} ·
              {{ status.rollbackTarget?.active ? '运行' : '停止' }} /
              {{ status.rollbackTarget?.enabled ? '开机启动' : '不开机启动' }}
            </p>
            <p v-else class="muted">暂无可恢复的上一份配置。</p>
            <el-button
              type="warning"
              plain
              :disabled="!status?.rollbackAvailable || conflict || disconnected"
              @click="service('rollback')"
              >恢复上一份配置与版本</el-button
            >
          </section>
          <section class="logs">
            <div class="section-heading">
              <div>
                <h2>近期管理日志</h2>
                <p class="muted">管理服务 journal，不含 worker 原始日志；手动刷新。</p>
              </div>
              <div class="button-row">
                <el-input-number
                  v-model="logLimit"
                  :min="1"
                  :max="500"
                  aria-label="日志条数"
                /><el-button @click="readLogs">读取日志</el-button>
              </div>
            </div>
            <el-alert
              v-if="logs?.truncated"
              title="日志达到条数或大小限制，部分内容已截断。"
              type="info"
              :closable="false"
            />
            <pre class="log-view" data-testid="logs">{{
              logs
                ? logs.entries.length
                  ? logs.entries.map((l) => `${l.time} [${l.unit}] ${l.message}`).join('\n')
                  : '暂无管理日志'
                : '点击“读取日志”查看近期记录。'
            }}</pre>
          </section>
        </el-tab-pane>
      </el-tabs>
    </main>
    <footer class="savebar">
      <div>
        <strong>{{ dirty ? '有未应用修改' : '配置已同步' }}</strong
        ><span class="muted">{{
          dirty ? (restart ? '应用时需要重启服务' : '应用时保留已有连接') : '运行状态每 5 秒刷新'
        }}</span>
      </div>
      <div>
        <el-button :disabled="!dirty || busy || refreshing" @click="reload">放弃修改</el-button
        ><el-button
          type="primary"
          :disabled="!dirty || busy || refreshing || conflict || disconnected"
          @click="apply"
          >校验并应用</el-button
        >
      </div>
    </footer>

    <el-drawer v-model="profilesOpen" title="管理代理池与直连" size="min(640px, 100vw)">
      <p class="hint">修改保存到页面草稿，关闭抽屉后点击“校验并应用”生效。</p>
      <el-form label-position="top"
        ><el-form-item label="应用后出口"
          ><el-select v-model="draft!.activeProfile" aria-label="应用后出口"
            ><el-option
              v-for="p in draft!.profiles"
              :key="p.name"
              :label="p.name"
              :value="p.name" /></el-select></el-form-item
      ></el-form>
      <div v-for="p in draft!.profiles" :key="p.name" class="profile-row">
        <div>
          <strong>{{ p.name }}</strong>
          <p class="muted">
            {{
              p.type === 'direct'
                ? '直连'
                : `${p.nodes?.length ?? 0} 个节点 · ${p.probe?.type?.toUpperCase() ?? '未设置'} 探测`
            }}
          </p>
        </div>
        <div>
          <el-button
            v-if="p.type === 'pool'"
            link
            :disabled="dirty || !status?.active || disconnected"
            @click="probe(p.name)"
            >探测</el-button
          ><el-button link type="primary" @click="editProfile(p)">编辑</el-button
          ><el-button link type="danger" @click="removeProfile(p.name)">删除</el-button>
        </div>
      </div>
      <el-button type="primary" plain @click="editProfile()">新建 profile</el-button>
    </el-drawer>
    <el-drawer
      v-model="profileOpen"
      :title="profileOrigin ? '编辑 profile' : '新建 profile'"
      size="min(500px, 100vw)"
      append-to-body
    >
      <el-form label-position="top"
        ><el-form-item label="名称"
          ><el-input
            v-model="profileEditor.name"
            aria-label="profile 名称"
            maxlength="64" /></el-form-item
        ><el-form-item label="转发方式"
          ><el-select
            v-model="profileEditor.type"
            aria-label="转发方式"
            @change="profileTypeChanged"
            ><el-option label="直连" value="direct" /><el-option
              label="代理池"
              value="pool" /></el-select
        ></el-form-item>
        <template v-if="profileEditor.type === 'pool' && profileEditor.probe"
          ><h2>端到端探测</h2>
          <el-form-item label="探测类型"
            ><el-select v-model="profileEditor.probe!.type" aria-label="探测类型"
              ><el-option label="TCP 建连" value="tcp" /><el-option
                label="HTTP / HTTPS 状态码"
                value="http" /></el-select></el-form-item
          ><el-form-item v-if="profileEditor.probe!.type === 'tcp'" label="目标地址"
            ><el-input
              v-model="profileEditor.probe!.addr"
              aria-label="探测目标地址"
              placeholder="example.com:443" /></el-form-item
          ><template v-else
            ><el-form-item label="目标 URL"
              ><el-input
                v-model="profileEditor.probe!.url"
                aria-label="探测目标 URL"
                placeholder="https://example.com/health" /></el-form-item
            ><el-form-item label="期望状态码"
              ><el-input-number
                v-model="profileEditor.probe!.expectedStatus"
                :min="100"
                :max="599"
                :value-on-clear="200" /></el-form-item></template
          ><el-form-item label="探测间隔（秒）"
            ><el-input-number
              v-model="profileEditor.probe!.interval"
              :min="1"
              :max="86400" /></el-form-item
          ><el-form-item label="探测超时（秒）"
            ><el-input-number
              v-model="profileEditor.probe!.timeout"
              :min="0.1"
              :max="60"
              :step="0.1"
          /></el-form-item>
          <p class="hint">
            新建代理池后，请添加至少一个节点再应用。修改 profile 名称后，上游认证密码需重新填写。
          </p></template
        > </el-form
      ><template #footer
        ><el-button @click="profileOpen = false">取消</el-button
        ><el-button type="primary" @click="saveProfile">保存到草稿</el-button></template
      >
    </el-drawer>
    <el-drawer
      v-model="nodeOpen"
      :title="nodeOrigin ? '编辑节点' : '添加节点'"
      size="min(500px, 100vw)"
      @closed="nodeEditor = { id: '', type: 'socks5', addr: '' }"
    >
      <el-form label-position="top"
        ><el-form-item label="所属代理池"
          ><el-select v-model="nodePool" aria-label="节点所属池"
            ><el-option
              v-for="p in pools"
              :key="p.name"
              :label="p.name"
              :value="p.name" /></el-select></el-form-item
        ><el-form-item label="节点 ID"
          ><el-input v-model="nodeEditor.id" aria-label="节点 ID" maxlength="64" /></el-form-item
        ><el-form-item label="代理类型"
          ><el-select v-model="nodeEditor.type" aria-label="代理类型"
            ><el-option label="HTTP CONNECT" value="http" /><el-option
              label="SOCKS5"
              value="socks5" /></el-select></el-form-item
        ><el-form-item label="上游地址"
          ><el-input
            v-model="nodeEditor.addr"
            aria-label="上游地址"
            placeholder="proxy.example:1080 或 [::1]:1080" /></el-form-item
        ><el-form-item label="上游认证"
          ><el-switch
            :model-value="!!nodeEditor.auth"
            aria-label="上游认证"
            @change="setNodeAuth" /></el-form-item
        ><template v-if="nodeEditor.auth"
          ><el-form-item label="用户名"
            ><el-input
              v-model="nodeEditor.auth.username"
              aria-label="上游用户名"
              autocomplete="off" /></el-form-item
          ><el-form-item label="密码"
            ><el-input
              v-model="nodeEditor.auth.password"
              aria-label="上游密码"
              type="password"
              autocomplete="new-password"
              :placeholder="
                nodeEditor.auth.passwordSet ? '已设置，留空保留原密码' : '必须填写密码'
              " /></el-form-item
        ></template>
        <p class="hint">
          HTTP 使用 CONNECT，到上游的连接不启用 TLS。修改所属池、节点 ID
          或认证用户名后，需要重新填写上游密码。
        </p></el-form
      >
      <template #footer
        ><el-button @click="nodeOpen = false">取消</el-button
        ><el-button type="primary" @click="saveNode">保存到草稿</el-button></template
      >
    </el-drawer>
  </div>
</template>
