<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { request } from './api'
defineProps<{ error?: string }>()
const emit = defineEmits<{ ready: [csrf: string] }>()
const initialized = ref(true),
  loaded = ref(false),
  busy = ref(false)
const password = ref(''),
  confirm = ref(''),
  code = ref(''),
  error = ref('')
onMounted(async () => {
  try {
    initialized.value = (
      await request<{ initialized: boolean }>('', '/auth/status')
    ).data.initialized
    if (initialized.value) {
      try {
        emit('ready', (await request<{ csrf: string }>('', '/auth/session')).data.csrf)
      } catch {
        /* show login */
      }
    }
    loaded.value = true
  } catch (e) {
    error.value = String(e)
  }
})
async function submit() {
  if (busy.value) return
  if (!initialized.value && password.value !== confirm.value) {
    error.value = '两次密码不一致'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const result = await request<{ csrf: string }>(
      '',
      initialized.value ? '/auth/login' : '/auth/initialize',
      'POST',
      initialized.value
        ? { password: password.value }
        : { code: code.value, password: password.value },
    )
    password.value = ''
    confirm.value = ''
    code.value = ''
    emit('ready', result.data.csrf)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '登录失败'
  } finally {
    busy.value = false
  }
}
</script>
<template>
  <div class="login-shell">
    <main class="login-card">
      <header class="login-brand">
        <span class="brand-mark lg">R</span>
        <h1>RelayHarbor</h1>
      </header>
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
      <el-alert
        v-else-if="$props.error"
        :title="$props.error"
        type="error"
        :closable="false"
        show-icon
      />
      <el-form v-if="loaded" label-position="top" size="large" @submit.prevent="submit">
        <el-form-item v-if="!initialized" label="一次性初始化码"
          ><el-input
            v-model="code"
            aria-label="一次性初始化码"
            type="password"
            autocomplete="off"
            placeholder="安装结束时显示"
        /></el-form-item>
        <el-form-item label="管理员密码"
          ><el-input
            v-model="password"
            aria-label="管理员密码"
            type="password"
            :autocomplete="initialized ? 'current-password' : 'new-password'"
            :placeholder="initialized ? '' : '12–128 个字符'"
            show-password
        /></el-form-item>
        <el-form-item v-if="!initialized" label="确认密码"
          ><el-input
            v-model="confirm"
            aria-label="确认密码"
            type="password"
            autocomplete="new-password"
        /></el-form-item>
        <el-button
          class="full"
          type="primary"
          native-type="submit"
          :loading="busy"
          :disabled="!password"
          >{{ initialized ? '登录控制台' : '设置密码并继续' }}</el-button
        >
      </el-form>
      <p class="login-foot">忘记密码：<code>gostctl auth reset</code></p>
    </main>
  </div>
</template>
