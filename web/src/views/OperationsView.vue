<script setup lang="ts">
import { ref } from 'vue'
import { request } from '../api'
import { useConsole } from '../console'

const { token, status, conflict, disconnected, logs, logLimit, service, readLogs, showError } =
  useConsole()
const password = ref(''),
  oldPassword = ref(''),
  changing = ref(false)
async function changePassword() {
  if (changing.value) return
  changing.value = true
  try {
    await request(token.value, '/auth/password', 'POST', {
      oldPassword: oldPassword.value,
      password: password.value,
    })
    oldPassword.value = ''
    password.value = ''
    location.reload()
  } catch (e) {
    showError(e)
  } finally {
    changing.value = false
  }
}
</script>

<template>
  <div class="page-stack">
    <div class="ops-grid">
      <section class="card">
        <header class="card-header">
          <h2>转发服务</h2>
          <span v-if="status?.enabled" class="badge">开机启动</span>
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
      </section>
      <section class="card">
        <header class="card-header">
          <div>
            <h2>回滚</h2>
            <p v-if="status?.rollbackAvailable" class="sub mono">
              GOST {{ status.rollbackTarget?.version }} ·
              {{ status.rollbackTarget?.activeProfile }} ·
              {{ status.rollbackTarget?.active ? '运行' : '停止' }}
            </p>
          </div>
        </header>
        <el-button
          type="warning"
          plain
          :disabled="!status?.rollbackAvailable || conflict || disconnected"
          @click="service('rollback')"
          >恢复上一份配置与版本</el-button
        >
      </section>
    </div>
    <section class="card">
      <header class="card-header">
        <h2>管理日志</h2>
        <div class="button-row">
          <el-input-number
            v-model="logLimit"
            :min="1"
            :max="500"
            aria-label="日志条数"
            controls-position="right"
          /><el-button @click="readLogs">读取日志</el-button>
        </div>
      </header>
      <el-alert
        v-if="logs?.truncated"
        title="日志达到条数或大小限制，部分内容已截断。"
        type="info"
        :closable="false"
      />
      <pre v-if="logs" class="code-block log-view" data-testid="logs">{{
        logs.entries.length
          ? logs.entries.map((l) => `${l.time} [${l.unit}] ${l.message}`).join('\n')
          : '暂无管理日志'
      }}</pre>
    </section>
    <section class="card">
      <details class="disclosure">
        <summary>修改管理员密码</summary>
        <el-form @submit.prevent="changePassword" label-position="top" class="form-grid">
          <el-form-item label="当前密码"
            ><el-input v-model="oldPassword" type="password" autocomplete="current-password"
          /></el-form-item>
          <el-form-item label="新密码"
            ><el-input v-model="password" type="password" autocomplete="new-password"
          /></el-form-item>
          <div class="form-actions">
            <el-button native-type="submit" :disabled="changing">修改并退出所有会话</el-button>
          </div>
        </el-form>
      </details>
    </section>
  </div>
</template>
