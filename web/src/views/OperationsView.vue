<script setup lang="ts">
import { useConsole } from '../console'

const { status, conflict, disconnected, logs, logLimit, serviceLabel, service, readLogs } =
  useConsole()
</script>

<template>
  <div class="page-stack">
    <div class="ops-grid">
      <section class="card">
        <header class="card-header">
          <div>
            <h2>转发服务</h2>
            <p>
              {{ serviceLabel }} · {{ status?.enabled ? '已开启开机启动' : '未开启开机启动' }} ·
              {{ status?.arch }}
            </p>
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
        <p class="hint">停止或重启会中断已有连接；管理页面仍可继续使用。</p>
      </section>
      <section class="card">
        <header class="card-header">
          <div>
            <h2>回滚</h2>
            <p v-if="status?.rollbackAvailable">
              目标 GOST {{ status.rollbackTarget?.version }} · 出口
              {{ status.rollbackTarget?.activeProfile }} ·
              {{ status.rollbackTarget?.active ? '运行' : '停止' }} /
              {{ status.rollbackTarget?.enabled ? '开机启动' : '不开机启动' }}
            </p>
            <p v-else>暂无可恢复的上一份配置。</p>
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
        <div>
          <h2>近期管理日志</h2>
          <p>管理服务 journal，不含 worker 原始日志；手动刷新。</p>
        </div>
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
      <pre class="code-block log-view" data-testid="logs">{{
        logs
          ? logs.entries.length
            ? logs.entries.map((l) => `${l.time} [${l.unit}] ${l.message}`).join('\n')
            : '暂无管理日志'
          : '点击“读取日志”查看近期记录。'
      }}</pre>
    </section>
  </div>
</template>
