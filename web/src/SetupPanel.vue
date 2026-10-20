<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
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
const credentials = ref<unknown>(),
  password = ref(''),
  oldPassword = ref('')
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
function finish() {
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
function changePassword() {
  void action(async () => {
    await request(props.token, '/auth/password', 'POST', {
      oldPassword: oldPassword.value,
      password: password.value,
    })
    oldPassword.value = ''
    password.value = ''
    location.reload()
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
  <section class="panel" aria-label="部署向导">
    <h2>部署设置</h2>
    <p>基础服务已启动。可直接进入控制台，或继续配置域名 HTTPS 和 AnyTLS。</p>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <el-alert
      v-if="job"
      :title="`${job.step} · ${job.status}`"
      :description="job.error"
      :type="job.status === 'failed' || job.status === 'interrupted' ? 'error' : 'info'"
      :closable="false"
    />
    <p v-if="state?.closeAt">
      正式 HTTPS 已就绪，请打开
      <a :href="state.url" rel="noreferrer">{{ state.url }}</a> 并用管理员密码重新登录。IP
      管理入口将在 {{ new Date(state.closeAt).toLocaleTimeString() }} 关闭。
    </p>
    <el-form label-position="top" style="max-width: 640px" @submit.prevent="start('tls')">
      <h3>域名 HTTPS</h3>
      <p>域名需指向本服务器，放行 TCP 80/443。使用专用服务器，已有外部 Nginx 不会被接管。</p>
      <el-form-item label="域名"
        ><el-input v-model="domain" aria-label="域名" placeholder="panel.example.com"
      /></el-form-item>
      <el-form-item label="邮箱"><el-input v-model="email" aria-label="邮箱" /></el-form-item>
      <el-checkbox v-model="accept"
        >我接受
        <a href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer"
          >Let's Encrypt 服务条款</a
        ></el-checkbox
      >
      <div>
        <el-button
          type="primary"
          native-type="submit"
          :disabled="busy || !accept || ['queued', 'running'].includes(job?.status ?? '')"
          >{{ tlsReady ? '更新 HTTPS 配置' : '配置 HTTPS' }}</el-button
        >
      </div>
    </el-form>
    <h3>AnyTLS 客户端订阅</h3>
    <p v-if="!tlsReady">先完成域名 HTTPS，才能启用 AnyTLS。</p>
    <el-button
      v-if="!anyReady"
      :disabled="!tlsReady || busy || ['queued', 'running'].includes(job?.status ?? '')"
      @click="start('anytls')"
      >初始化 AnyTLS</el-button
    >
    <AnyTLSPanel v-else :token="token" @error="emit('error', $event)" />
    <h3>基础模式</h3>
    <p>默认直连，代理池可以进入控制台后配置。SOCKS 和 AnyTLS 实际使用的端口需要自行放行。</p>
    <el-button @click="showCredentials">查看 SOCKS 连接信息</el-button>
    <pre v-if="credentials">{{ JSON.stringify(credentials, null, 2) }}</pre>
    <el-button type="primary" :disabled="busy" @click="finish">进入控制台</el-button>
    <details>
      <summary>修改管理员密码</summary>
      <el-form @submit.prevent="changePassword" label-position="top">
        <el-form-item label="当前密码"
          ><el-input v-model="oldPassword" type="password" autocomplete="current-password"
        /></el-form-item>
        <el-form-item label="新密码"
          ><el-input v-model="password" type="password" autocomplete="new-password"
        /></el-form-item>
        <el-button native-type="submit" :disabled="busy">修改并退出所有会话</el-button>
      </el-form>
    </details>
  </section>
</template>
