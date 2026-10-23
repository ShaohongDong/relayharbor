<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { APIError, request } from './api'

interface User {
  id: string
  name: string
  port: number
  enabled: boolean
  active: boolean
  profile: string
}
interface Status {
  configured: boolean
  enabled: boolean
  domain?: string
  version?: string
  users: User[]
}
interface Job {
  id: string
  kind?: string
  status: string
  step: string
  error?: string
}
interface Credentials {
  password: string
  url: string
  provider: string
}
const props = defineProps<{ token: string; embedded?: boolean }>()
const emit = defineEmits<{ error: [error: unknown] }>()
const status = ref<Status>()
const revision = ref('')
const error = ref('')
const busy = ref(false)
const loading = ref(false)
const editor = ref(false)
const renameID = ref('')
const draft = ref({ id: '', name: '', port: 0 })
const credentials = ref<Credentials>()
const secretUser = ref('')
const conflict = ref(false)
const job = ref<Job>()
const tlsEnabled = ref(false)
const profiles = ref<string[]>([])
const running = () => ['queued', 'running'].includes(job.value?.status ?? '')
let alive = true
let timer: ReturnType<typeof setInterval> | undefined

function failure(e: unknown) {
  error.value = e instanceof Error ? e.message : '操作失败，请刷新状态确认结果。'
  if (e instanceof APIError && e.status === 401) emit('error', e)
  if (e instanceof APIError && e.code === 'revision_conflict') conflict.value = true
}
async function refresh() {
  if (loading.value || busy.value || editor.value || !alive) return
  loading.value = true
  try {
    if (running())
      job.value = (await request<Job>(props.token, `/setup/jobs/${job.value!.id}`)).data
    const result = await request<Status>(props.token, '/anytls')
    const names = (await request<{ name: string }[]>(props.token, '/profiles')).data
    if (!result.data.configured && !props.embedded) {
      tlsEnabled.value = (await request<{ enabled: boolean }>(props.token, '/tls')).data.enabled
      // Resume an initialization started earlier, e.g. before a page reload.
      if (!job.value) {
        const latest = (await request<{ job?: Job }>(props.token, '/setup')).data.job
        if (latest?.kind === 'anytls') job.value = latest
      }
    }
    if (alive && !editor.value && !busy.value) {
      status.value = result.data
      profiles.value = names.map((p) => p.name)
      revision.value = result.revision
      conflict.value = false
      error.value = ''
    }
  } catch (e) {
    if (alive) failure(e)
  } finally {
    loading.value = false
  }
}
function openEditor(user?: User) {
  credentials.value = undefined
  renameID.value = user?.id ?? ''
  draft.value = { id: user?.id ?? '', name: user?.name ?? '', port: 0 }
  editor.value = true
}
async function mutate(path: string, body: unknown = {}) {
  if (busy.value || conflict.value) return false
  busy.value = true
  error.value = ''
  credentials.value = undefined
  try {
    const result = await request(props.token, `/anytls/${path}`, 'POST', body, revision.value)
    revision.value = result.revision
    ElMessage.success('操作已完成')
    return true
  } catch (e) {
    failure(e)
    return false
  } finally {
    busy.value = false
  }
}
// Initialization reuses the background setup job, which needs the core config revision.
async function initialize() {
  if (busy.value || running()) return
  busy.value = true
  error.value = ''
  try {
    const setup = await request(props.token, '/setup')
    job.value = (
      await request<Job>(
        props.token,
        '/anytls/setup',
        'POST',
        {},
        setup.revision,
        crypto.randomUUID(),
      )
    ).data
  } catch (e) {
    failure(e)
  } finally {
    busy.value = false
  }
}
async function save() {
  const id = draft.value.id
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(id) || !draft.value.name.trim()) {
    error.value = '请填写有效的用户 ID 和名称'
    return
  }
  const ok = renameID.value
    ? await mutate(`users/${id}/rename`, { name: draft.value.name })
    : await mutate('users', draft.value)
  if (ok) {
    editor.value = false
    await refresh()
  }
}
async function operate(op: string, user?: User) {
  const descriptions: Record<string, string> = {
    disable: user
      ? '停止此用户的连接并撤销订阅访问？其他用户保持连接。'
      : '停止全部 AnyTLS 用户并停用订阅？',
    delete: '删除此用户并终止其现有连接？其他用户保持连接。',
    'reset-password': '重置此用户密码并断开其现有连接？请让客户端更新订阅，其他用户保持连接。',
    'reset-token': '使此用户旧订阅链接立即失效？代理密码及已有连接保持有效。',
  }
  if (descriptions[op]) {
    try {
      await ElMessageBox.confirm(descriptions[op], '确认操作', {
        confirmButtonText: '确认',
        cancelButtonText: '取消',
      })
    } catch {
      return
    }
  }
  if (await mutate(user ? `users/${user.id}/${op}` : op)) await refresh()
}
// The egress travels in the instance's gateway credential, so changing it restarts only this user.
async function route(user: User, profile: string) {
  if (profile === user.profile) return
  try {
    await ElMessageBox.confirm(
      `将 ${user.name} 的出口改为 ${profile || '跟随当前 profile'}？此用户实例会重启并断开其现有连接，其他用户保持连接。`,
      '确认切换出口',
      { confirmButtonText: '确认', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  if (await mutate(`users/${user.id}/route`, { profile })) await refresh()
}
async function reveal(user: User) {
  busy.value = true
  credentials.value = undefined
  error.value = ''
  try {
    const result = await request<Credentials>(
      props.token,
      `/anytls/users/${user.id}/credentials`,
      'POST',
      {},
      revision.value,
    )
    credentials.value = result.data
    secretUser.value = user.name
  } catch (e) {
    failure(e)
  } finally {
    busy.value = false
  }
}
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.error('无法访问剪贴板，请手动复制')
  }
}
async function download() {
  if (!credentials.value) return
  try {
    const url = new URL(credentials.value.url)
    // Keep requests on the authenticated console's origin (also works via SSH).
    const response = await fetch(url.pathname, { cache: 'no-store', credentials: 'omit' })
    if (!response.ok) throw new Error('下载失败；订阅需通过已配置的 HTTPS 域名访问')
    const blob = URL.createObjectURL(await response.blob())
    const a = document.createElement('a')
    a.href = blob
    a.download = 'clash.yaml'
    a.click()
    setTimeout(() => URL.revokeObjectURL(blob), 1000)
  } catch (e) {
    failure(e)
  }
}
onMounted(() => {
  void refresh()
  timer = setInterval(() => {
    if (!document.hidden && !credentials.value) void refresh()
  }, 5000)
})
onBeforeUnmount(() => {
  alive = false
  credentials.value = undefined
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div :class="embedded ? 'anytls-embedded' : 'page-stack'" data-testid="anytls-panel">
    <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
    <el-alert
      v-if="conflict"
      title="配置已变化。输入已保留，请关闭编辑并刷新状态后重试。"
      type="warning"
      :closable="false"
      show-icon
    />
    <section :class="embedded ? '' : 'card card-flush'">
      <header class="card-header" :class="{ padded: !embedded }">
        <div>
          <h2>{{ embedded ? 'AnyTLS 用户' : '客户端订阅' }}</h2>
          <p v-if="status?.configured" class="sub">
            <span class="health" :class="status.enabled ? 'tone-ok' : 'tone-idle'"
              ><span class="status-dot" /><span class="mono">{{ status.domain }}</span></span
            >
          </p>
        </div>
        <div class="button-row">
          <el-button :disabled="editor || busy" :loading="loading" @click="refresh"
            >刷新订阅状态</el-button
          >
          <template v-if="status?.configured">
            <el-button
              :disabled="busy || conflict || loading"
              @click="operate(status.enabled ? 'disable' : 'enable')"
              >{{ status.enabled ? '停用 AnyTLS' : '启用 AnyTLS' }}</el-button
            >
            <el-button
              type="primary"
              :disabled="!status.enabled || busy || conflict || loading"
              @click="openEditor()"
              >新增用户 / 设备</el-button
            >
          </template>
        </div>
      </header>
      <template v-if="status?.configured">
        <el-table :data="status.users" empty-text="暂无用户，请添加用户或设备">
          <el-table-column prop="name" label="用户 / 设备" min-width="150"
            ><template #default="{ row }"
              ><span class="strong">{{ row.name }}</span></template
            ></el-table-column
          >
          <el-table-column prop="id" label="ID" min-width="120"
            ><template #default="{ row }"
              ><span class="mono">{{ row.id }}</span></template
            ></el-table-column
          >
          <el-table-column prop="port" label="TCP 端口" width="100"
            ><template #default="{ row }"
              ><span class="mono">{{ row.port }}</span></template
            ></el-table-column
          >
          <el-table-column label="出口" min-width="170">
            <template #default="{ row }"
              ><el-select
                :model-value="row.profile"
                :aria-label="'出口 ' + row.id"
                :disabled="busy || conflict || loading"
                size="small"
                @change="(v: string) => route(row, v)"
                ><el-option label="跟随当前 profile" value="" /><el-option
                  v-for="p in profiles"
                  :key="p"
                  :label="p"
                  :value="p" /></el-select
            ></template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }"
              ><span
                class="health"
                :class="!row.enabled ? 'tone-idle' : row.active ? 'tone-ok' : 'tone-warn'"
                ><span class="status-dot" />{{
                  !row.enabled ? '已禁用' : row.active ? '运行中' : '已停止'
                }}</span
              ></template
            >
          </el-table-column>
          <el-table-column label="操作" min-width="300" fixed="right">
            <template #default="{ row }">
              <el-button
                link
                type="primary"
                :disabled="busy || conflict || loading || !status.enabled || !row.enabled"
                @click="reveal(row)"
                >订阅与凭据</el-button
              >
              <el-button link :disabled="busy || conflict || loading" @click="openEditor(row)"
                >重命名</el-button
              >
              <el-button
                link
                :disabled="busy || conflict || loading || (!row.enabled && !status.enabled)"
                @click="operate(row.enabled ? 'disable' : 'enable', row)"
                >{{ row.enabled ? '禁用' : '启用' }}</el-button
              >
              <el-dropdown
                trigger="click"
                :disabled="busy || conflict || loading"
                @command="(op: string) => operate(op, row)"
              >
                <el-button link :disabled="busy || conflict || loading">更多</el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="reset-token">重置链接</el-dropdown-item>
                    <el-dropdown-item command="reset-password">重置密码</el-dropdown-item>
                    <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </template>
          </el-table-column>
        </el-table>
        <p class="hint" :class="{ padded: !embedded }">需在防火墙放行各用户的 TCP 端口。</p>
      </template>
      <div v-else-if="status" class="empty-state">
        <strong>AnyTLS 尚未初始化</strong>
        <p v-if="!tlsEnabled" class="muted">需先完成 HTTPS</p>
        <el-alert
          v-if="job"
          :title="`${job.step} · ${job.status}`"
          :description="job.error"
          :type="job.status === 'failed' || job.status === 'interrupted' ? 'error' : 'info'"
          :closable="false"
          show-icon
        />
        <el-button
          v-if="!embedded"
          type="primary"
          :disabled="!tlsEnabled || busy || running()"
          @click="initialize"
          >初始化 AnyTLS</el-button
        >
      </div>
    </section>
    <el-drawer
      v-model="editor"
      :title="renameID ? '重命名用户' : '新增用户 / 设备'"
      size="min(440px, 100vw)"
      :close-on-click-modal="false"
      :before-close="
        (done: () => void) => {
          if (!busy) done()
        }
      "
    >
      <el-form label-position="top" @submit.prevent="save">
        <el-form-item label="用户 ID"
          ><el-input v-model="draft.id" :disabled="!!renameID || busy" maxlength="64"
        /></el-form-item>
        <el-form-item label="显示名称"
          ><el-input v-model="draft.name" :disabled="busy" maxlength="128"
        /></el-form-item>
        <el-form-item v-if="!renameID" label="TCP 端口（0 为自动分配）"
          ><el-input-number v-model="draft.port" :disabled="busy" :min="0" :max="65535"
        /></el-form-item>
      </el-form>
      <p v-if="conflict" role="alert" class="callout callout-warn">
        配置已变化，输入仍保留。关闭编辑并刷新状态后重试。
      </p>
      <template #footer>
        <el-button :disabled="busy" @click="editor = false">取消</el-button>
        <el-button type="primary" :loading="busy" :disabled="conflict" @click="save"
          >保存用户</el-button
        >
      </template>
    </el-drawer>
    <el-dialog
      :model-value="!!credentials"
      :title="`${secretUser} · 订阅与凭据`"
      width="min(560px, calc(100vw - 32px))"
      @close="credentials = undefined"
    >
      <template v-if="credentials">
        <el-alert
          title="订阅链接包含此用户的访问权限，请仅交给对应使用者。"
          type="warning"
          :closable="false"
          show-icon
        />
        <el-form label-position="top" class="credential-form">
          <el-form-item label="完整 Clash 配置"
            ><div class="copy-line">
              <el-input :model-value="credentials.url" readonly /><el-button
                @click="copy(credentials.url)"
                >复制订阅链接</el-button
              >
            </div></el-form-item
          >
          <el-form-item label="Proxy Provider 节点列表"
            ><div class="copy-line">
              <el-input :model-value="credentials.provider" readonly /><el-button
                @click="copy(credentials.provider)"
                >复制 Provider 链接</el-button
              >
            </div></el-form-item
          >
          <el-form-item label="AnyTLS 密码"
            ><el-input :model-value="credentials.password" type="password" show-password readonly
          /></el-form-item>
        </el-form>
      </template>
      <template #footer>
        <el-button type="primary" @click="download">下载完整配置</el-button>
      </template>
    </el-dialog>
  </div>
</template>
