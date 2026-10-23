<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { request } from './api'
import AnyTLSPanel from './AnyTLSPanel.vue'
const props = defineProps<{ token: string }>()
const emit = defineEmits<{ done: []; error: [error: unknown] }>()
interface Job {
  id: string
  status: string
  step: string
  error?: string
}
interface Setup {
  completed: boolean
  url: string
  temporaryURL: string
  closeAt?: string
  job?: Job
}
const state = ref<Setup>(),
  revision = ref(''),
  domain = ref(''),
  email = ref(''),
  accept = ref(false)
const busy = ref(false),
  error = ref(''),
  job = ref<Job>(),
  tlsReady = ref(false),
  anyReady = ref(false)
const credentials = ref<unknown>()
let timer: ReturnType<typeof setInterval>
async function refresh() {
  const s = await request<Setup>(props.token, '/setup')
  state.value = s.data
  revision.value = s.revision
  job.value = s.data.job
  if (!job.value || !['queued', 'running'].includes(job.value.status)) {
    tlsReady.value = (await request<{ enabled: boolean }>(props.token, '/tls')).data.enabled
    anyReady.value = (
      await request<{ configured: boolean }>(props.token, '/anytls')
    ).data.configured
  }
}
async function action(fn: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await fn()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '操作失败'
    emit('error', e)
  } finally {
    busy.value = false
  }
}
function start(kind: 'tls' | 'anytls') {
  void action(async () => {
    const data =
      kind === 'tls' ? { domain: domain.value, email: email.value, acceptTos: accept.value } : {}
    job.value = (
      await request<Job>(
        props.token,
        `/${kind}/setup`,
        'POST',
        data,
        revision.value,
        crypto.randomUUID(),
      )
    ).data
  })
}
async function finish() {
  try {
    await ElMessageBox.confirm(
      '完成后部署设置页面将永久关闭，之后修改域名需使用服务器 CLI。',
      '完成部署设置',
      { confirmButtonText: '进入控制台', cancelButtonText: '返回', type: 'warning' },
    )
  } catch {
    return
  }
  void action(async () => {
    await request(props.token, '/setup/complete', 'POST', {}, revision.value)
    emit('done')
  })
}
function showCredentials() {
  void action(async () => {
    credentials.value = (
      await request(props.token, '/setup/credentials', 'POST', {}, revision.value)
    ).data
  })
}
onMounted(() => {
  void action(refresh)
  timer = setInterval(() => {
    if (!document.hidden && !busy.value) void action(refresh)
  }, 3000)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  credentials.value = undefined
})
</script>
<template>
  <section class="page-stack setup" aria-label="部署向导">
    <div class="callout callout-info"><strong>基础服务已启动</strong></div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
    <el-alert
      v-if="job"
      :title="`${job.step} · ${job.status}`"
      :description="job.error"
      :type="job.status === 'failed' || job.status === 'interrupted' ? 'error' : 'info'"
      :closable="false"
      show-icon
    />
    <el-alert v-if="state?.closeAt" type="success" :closable="false" show-icon
      ><template #title
        >正式 HTTPS 已就绪，请打开
        <a :href="state.url" rel="noreferrer">{{ state.url }}</a> 并用管理员密码重新登录。IP
        管理入口将在 {{ new Date(state.closeAt).toLocaleTimeString() }} 关闭。</template
      ></el-alert
    >
    <section class="card step" :class="{ done: tlsReady }">
      <header class="card-header">
        <span class="step-num">1</span>
        <div>
          <h2>域名 HTTPS</h2>
          <p class="sub">域名指向本机，放行 TCP 80/443</p>
        </div>
        <span v-if="tlsReady" class="badge badge-ok">已启用</span>
      </header>
      <el-form label-position="top" class="form-grid" @submit.prevent="start('tls')">
        <el-form-item label="域名"
          ><el-input v-model="domain" aria-label="域名" placeholder="panel.example.com"
        /></el-form-item>
        <el-form-item label="邮箱"><el-input v-model="email" aria-label="邮箱" /></el-form-item>
        <div class="form-actions">
          <el-checkbox v-model="accept"
            >我接受
            <a href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer"
              >Let's Encrypt 服务条款</a
            ></el-checkbox
          >
          <el-button
            type="primary"
            native-type="submit"
            :disabled="busy || !accept || ['queued', 'running'].includes(job?.status ?? '')"
            >{{ tlsReady ? '更新 HTTPS 配置' : '配置 HTTPS' }}</el-button
          >
        </div>
      </el-form>
    </section>
    <section class="card step" :class="{ done: anyReady }">
      <header class="card-header">
        <span class="step-num">2</span>
        <div>
          <h2>AnyTLS 客户端订阅</h2>
          <p v-if="!tlsReady" class="sub">需先完成域名 HTTPS</p>
        </div>
      </header>
      <el-button
        v-if="!anyReady"
        :disabled="!tlsReady || busy || ['queued', 'running'].includes(job?.status ?? '')"
        @click="start('anytls')"
        >初始化 AnyTLS</el-button
      >
      <AnyTLSPanel v-else :token="token" embedded @error="emit('error', $event)" />
    </section>
    <section class="card step">
      <header class="card-header">
        <span class="step-num">3</span>
        <div>
          <h2>基础模式</h2>
        </div>
      </header>
      <div class="button-row">
        <el-button @click="showCredentials">查看 SOCKS 连接信息</el-button>
        <el-button type="primary" :disabled="busy" @click="finish">进入控制台</el-button>
      </div>
      <pre v-if="credentials" class="code-block">{{ JSON.stringify(credentials, null, 2) }}</pre>
    </section>
  </section>
</template>
