<script setup lang="ts">
import { useConsole } from '../console'

const { nodeOpen, nodeOrigin, nodeEditor, nodePool, pools, saveNode, setNodeAuth } = useConsole()
</script>

<template>
  <el-drawer
    v-model="nodeOpen"
    :title="nodeOrigin ? '编辑节点' : '添加节点'"
    size="min(480px, 100vw)"
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
        ><el-switch :model-value="!!nodeEditor.auth" aria-label="上游认证" @change="setNodeAuth"
      /></el-form-item>
      <div v-if="nodeEditor.auth" class="form-pair">
        <el-form-item label="用户名"
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
            :placeholder="nodeEditor.auth.passwordSet ? '留空保留原密码' : ''"
        /></el-form-item>
      </div>
      <p v-if="nodeOrigin && nodeEditor.auth" class="hint">
        修改池、ID 或用户名后需重新填写密码
      </p></el-form
    >
    <template #footer
      ><el-button @click="nodeOpen = false">取消</el-button
      ><el-button type="primary" @click="saveNode">保存到草稿</el-button></template
    >
  </el-drawer>
</template>
