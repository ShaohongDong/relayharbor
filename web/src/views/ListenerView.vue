<script setup lang="ts">
import { useConsole } from '../console'

const { draft, status } = useConsole()
function addEntry() {
  draft.value!.entries ??= []
  draft.value!.entries.push({
    name: '',
    addr: '',
    udp: false,
    users: [{ username: '', password: '' }],
    profile: draft.value!.activeProfile,
  })
}
function profileType(name: string) {
  return draft.value!.profiles.find((p) => p.name === name)?.type
}
function entryAvailable(name: string) {
  return status.value?.entries?.find((e) => e.name === name)?.available
}
</script>

<template>
  <div class="page-stack narrow">
    <section class="card">
      <header class="card-header">
        <h2>SOCKS5 入口</h2>
      </header>
      <el-form label-position="top" class="form-grid"
        ><el-form-item label="监听地址"
          ><el-input
            v-model="draft!.listener.addr"
            aria-label="监听地址"
            placeholder="[::]:1080"
          /><span class="hint">[::] 同时接受 IPv4/IPv6；0.0.0.0 仅 IPv4</span></el-form-item
        ><el-form-item label="UDP 转发"
          ><div class="switch-line">
            <el-switch v-model="draft!.listener.udp" aria-label="UDP 转发" /><span class="hint"
              >仅直连</span
            >
          </div></el-form-item
        ></el-form
      >
    </section>
    <section class="card">
      <header class="card-header">
        <h2>入口用户</h2>
        <el-button @click="draft!.listener.users.push({ username: '', password: '' })"
          >添加入口用户</el-button
        >
      </header>
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
          :placeholder="u.passwordSet ? '留空保留原密码' : '密码'"
        /><el-button
          type="danger"
          plain
          :disabled="draft!.listener.users.length <= 1"
          @click="draft!.listener.users.splice(i, 1)"
          >移除</el-button
        >
      </div>
    </section>
    <section class="card">
      <header class="card-header">
        <h2>附加入口（多出口）</h2>
        <el-button @click="addEntry">添加附加入口</el-button>
      </header>
      <p class="hint">
        每个附加入口独立监听端口与用户，固定走所选
        profile，与共享入口的“应用后出口”同时生效。修改出口为热切换并保留已有连接；修改地址、用户或
        UDP 需要重启转发服务。
      </p>
      <div v-for="(e, i) in draft!.entries ?? []" :key="i" class="entry-block">
        <el-form label-position="top" class="form-grid"
          ><el-form-item label="名称"
            ><el-input
              v-model="e.name"
              :aria-label="'附加入口名称 ' + (i + 1)"
              placeholder="office"
          /></el-form-item>
          <el-form-item label="监听地址"
            ><el-input
              v-model="e.addr"
              :aria-label="'附加入口地址 ' + (i + 1)"
              placeholder="[::]:1081"
          /></el-form-item>
          <el-form-item label="出口 profile"
            ><el-select v-model="e.profile" :aria-label="'附加入口出口 ' + (i + 1)"
              ><el-option
                v-for="p in draft!.profiles"
                :key="p.name"
                :label="p.name"
                :value="p.name" /></el-select
            ><span v-if="entryAvailable(e.name) === false" class="hint">当前出口不可用</span>
          </el-form-item>
          <el-form-item label="UDP 转发"
            ><div class="switch-line">
              <el-switch
                v-model="e.udp"
                :aria-label="'附加入口 UDP ' + (i + 1)"
                :disabled="profileType(e.profile) !== 'direct' && !e.udp"
              /><span class="hint">仅直连出口</span>
            </div></el-form-item
          ></el-form
        >
        <div v-for="(u, j) in e.users" :key="j" class="user-row">
          <el-input
            v-model="u.username"
            :aria-label="`附加入口 ${i + 1} 用户名 ${j + 1}`"
            placeholder="用户名"
            autocomplete="off"
          /><el-input
            v-model="u.password"
            :aria-label="`附加入口 ${i + 1} 密码 ${j + 1}`"
            type="password"
            autocomplete="new-password"
            :placeholder="u.passwordSet ? '留空保留原密码' : '密码'"
          /><el-button
            type="danger"
            plain
            :disabled="e.users.length <= 1"
            @click="e.users.splice(j, 1)"
            >移除</el-button
          >
        </div>
        <div class="entry-actions">
          <el-button @click="e.users.push({ username: '', password: '' })">添加用户</el-button
          ><el-button type="danger" plain @click="draft!.entries!.splice(i, 1)"
            >删除附加入口</el-button
          >
        </div>
      </div>
    </section>
  </div>
</template>
