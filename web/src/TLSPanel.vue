<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { APIError, request } from './api'
import type { TLSStatus } from './model'

const props = defineProps<{ token: string; revision: string; blocked: boolean }>()
const emit = defineEmits<{ error: [error: unknown] }>()
const status = ref<TLSStatus>(),
  error = ref(''),
  loading = ref(false),
  submitting = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
let alive = true
const warning = computed(() => {
  const s = status.value
  if (!s?.configured) return ''
  if (s.warning) return s.warning
  if (s.lastResult?.error) return s.lastResult.error
  if (s.enabled && (!s.timerActive || !s.timerEnabled))
    return '自动续期定时任务未运行，请在服务器检查 systemd timer。'
  if (s.expiring) return '证书将在 14 天内到期或已过期，请检查自动续期结果。'
  if (s.enabled && !s.nginxActive) return 'Nginx 未运行，请在服务器检查服务状态。'
  return ''
})
const stamp = (value?: string) => (value ? new Date(value).toLocaleString() : '暂无')
async function refresh() {
  if (loading.value || !alive) return
  loading.value = true
  try {
    const result = await request<TLSStatus>(props.token, '/tls')
    if (alive) {
      status.value = result.data
      error.value = ''
    }
  } catch (e) {
    if (alive) {
      error.value = e instanceof Error ? e.message : 'HTTPS 状态读取失败'
      if (e instanceof APIError && e.status === 401) emit('error', e)
    }
  } finally {
    loading.value = false
  }
}
async function operate(op: 'renew' | 'reload') {
  if (submitting.value || status.value?.running || props.blocked) return
  if (op === 'reload') {
    try {
      await ElMessageBox.confirm('验证配置并平滑重载 Nginx？', '重载 Nginx', {
        confirmButtonText: '确认',
        cancelButtonText: '取消',
      })
    } catch {
      return
    }
  }
  submitting.value = true
  error.value = ''
  try {
    await request(props.token, `/tls/${op}`, 'POST', {}, props.revision)
    ElMessage.success('后台任务已提交，将自动刷新结果。')
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '操作失败；请刷新状态确认结果。'
    if (e instanceof APIError && (e.status === 401 || e.code === 'revision_conflict'))
      emit('error', e)
  } finally {
    submitting.value = false
  }
}
onMounted(() => {
  void refresh()
  timer = setInterval(() => {
    if (!document.hidden && !submitting.value) void refresh()
  }, 5000)
})
onBeforeUnmount(() => {
  alive = false
  if (timer) clearInterval(timer)
})
</script>

<template>
  <section class="operations" data-testid="tls-panel">
    <div class="section-heading">
      <h2>HTTPS 与证书</h2>
      <el-button :loading="loading" @click="refresh">刷新 HTTPS 状态</el-button>
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <el-alert v-if="warning" :title="warning" type="warning" :closable="false" />
    <template v-if="status?.configured">
      <p>
        <strong>{{ status.domain }}</strong> ·
        {{ status.enabled ? 'HTTPS 已启用' : 'HTTPS 未启用' }} ·
        {{ status.nginxActive ? 'Nginx 运行中' : 'Nginx 已停止' }}
      </p>
      <dl class="tls-details">
        <dt>证书签发者</dt>
        <dd>{{ status.issuer || '暂无证书' }}</dd>
        <dt>有效期</dt>
        <dd>{{ stamp(status.notBefore) }} 至 {{ stamp(status.notAfter) }}</dd>
        <dt>自动续期</dt>
        <dd>
          {{ status.timerEnabled && status.timerActive ? '已启用' : '未运行' }} · 下次检查
          {{ status.nextCheck || '暂无' }}
        </dd>
        <dt>最近结果</dt>
        <dd>
          {{
            status.lastResult
              ? `${stamp(status.lastResult.finishedAt)} · ${status.lastResult.error ? '失败' : '成功'} · ${status.lastResult.acquired ? '证书已获取' : '未获取新证书'} · ${status.lastResult.deployed ? '已部署' : '未部署新证书'}`
              : '暂无记录'
          }}
        </dd>
      </dl>
      <p v-if="status.running" role="status">
        {{
          status.operation === 'renew' ? '正在检查证书续期' : '正在重载 Nginx'
        }}，关闭页面不影响后台任务。
      </p>
      <p class="muted">
        每 12 小时自动检查续期；未到续期时间不会重复签发。域名修改与停用通过服务器 CLI 完成。
      </p>
      <div class="button-row">
        <el-button
          :disabled="!status.enabled || status.running || submitting || blocked"
          @click="operate('renew')"
          >检查续期</el-button
        >
        <el-button
          :disabled="!status.enabled || status.running || submitting || blocked"
          @click="operate('reload')"
          >重载 Nginx</el-button
        >
      </div>
    </template>
    <template v-else-if="status">
      <p class="muted">
        尚未配置 HTTPS。将域名指向服务器，放行公网 80/443 端口后，在专用服务器执行：
      </p>
      <pre class="config-preview">
sudo gostctl tls setup --domain panel.example.com --email admin@example.com --accept-tos</pre>
      <p class="muted">首次部署、域名修改及停用需通过 CLI；自动续期无需保持页面打开。</p>
    </template>
    <p v-else-if="loading" class="muted">正在读取 HTTPS 状态…</p>
  </section>
</template>
