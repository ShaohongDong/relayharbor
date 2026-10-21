<script setup lang="ts">
import { useConsole } from '../console'

const { draft, status, dirty, disconnected, profilesOpen, probe, editProfile, removeProfile } =
  useConsole()
</script>

<template>
  <el-drawer v-model="profilesOpen" title="管理代理池与直连" size="min(560px, 100vw)">
    <p class="hint">修改保存到页面草稿，关闭抽屉后点击“校验并应用”生效。</p>
    <el-form label-position="top"
      ><el-form-item label="应用后出口"
        ><el-select v-model="draft!.activeProfile" aria-label="应用后出口"
          ><el-option
            v-for="p in draft!.profiles"
            :key="p.name"
            :label="p.name"
            :value="p.name" /></el-select></el-form-item
    ></el-form>
    <ul class="profile-list">
      <li v-for="p in draft!.profiles" :key="p.name" class="profile-row">
        <div>
          <div class="pool-name">
            <strong>{{ p.name }}</strong>
            <span class="badge">{{ p.type === 'direct' ? '直连' : '代理池' }}</span>
          </div>
          <p>
            {{
              p.type === 'direct'
                ? '直接连接目标'
                : `${p.nodes?.length ?? 0} 个节点 · ${p.probe?.type?.toUpperCase() ?? '未设置'} 探测`
            }}
          </p>
        </div>
        <div class="row-actions">
          <el-button
            v-if="p.type === 'pool'"
            link
            :disabled="dirty || !status?.active || disconnected"
            @click="probe(p.name)"
            >探测</el-button
          ><el-button link type="primary" @click="editProfile(p)">编辑</el-button
          ><el-button link type="danger" @click="removeProfile(p.name)">删除</el-button>
        </div>
      </li>
    </ul>
    <el-button type="primary" plain @click="editProfile()">新建 profile</el-button>
  </el-drawer>
</template>
