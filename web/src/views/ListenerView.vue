<script setup lang="ts">
import { useConsole } from '../console'

const { draft } = useConsole()
</script>

<template>
  <div class="page-stack narrow">
    <section class="card">
      <header class="card-header">
        <div>
          <h2>SOCKS5 入口</h2>
          <p>入口认证始终必需。此处修改应用后需要重启转发服务。</p>
        </div>
      </header>
      <el-form label-position="top" class="form-grid"
        ><el-form-item label="监听地址"
          ><el-input
            v-model="draft!.listener.addr"
            aria-label="监听地址"
            placeholder="0.0.0.0:1080" /></el-form-item
        ><el-form-item label="UDP 转发"
          ><div class="switch-line">
            <el-switch v-model="draft!.listener.udp" aria-label="UDP 转发" /><span class="hint"
              >仅直连 profile 支持 UDP</span
            >
          </div></el-form-item
        ></el-form
      >
    </section>
    <section class="card">
      <header class="card-header">
        <div>
          <h2>入口用户</h2>
          <p>客户端连接 SOCKS5 入口时使用的账号。已设置的密码留空即保留。</p>
        </div>
        <el-button @click="draft!.listener.users.push({ username: '', password: '' })"
          >添加入口用户</el-button
        >
      </header>
      <div v-for="(u, i) in draft!.listener.users" :key="i" class="user-row">
        <span class="user-index">{{ i + 1 }}</span>
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
    </section>
  </div>
</template>
