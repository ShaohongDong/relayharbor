<script setup lang="ts">
import { useConsole } from '../console'

const { profileOpen, profileOrigin, profileEditor, profileTypeChanged, saveProfile } = useConsole()
</script>

<template>
  <el-drawer
    v-model="profileOpen"
    :title="profileOrigin ? '编辑 profile' : '新建 profile'"
    size="min(480px, 100vw)"
    append-to-body
  >
    <el-form label-position="top"
      ><el-form-item label="名称"
        ><el-input
          v-model="profileEditor.name"
          aria-label="profile 名称"
          maxlength="64" /></el-form-item
      ><el-form-item label="转发方式"
        ><el-radio-group
          v-model="profileEditor.type"
          aria-label="转发方式"
          @change="profileTypeChanged"
          ><el-radio-button label="直连" value="direct" /><el-radio-button
            label="代理池"
            value="pool" /></el-radio-group
      ></el-form-item>
      <template v-if="profileEditor.type === 'pool' && profileEditor.probe"
        ><h3 class="form-section">端到端探测</h3>
        <el-form-item label="探测类型"
          ><el-select v-model="profileEditor.probe!.type" aria-label="探测类型"
            ><el-option label="TCP 建连" value="tcp" /><el-option
              label="HTTP / HTTPS 状态码"
              value="http" /></el-select></el-form-item
        ><el-form-item v-if="profileEditor.probe!.type === 'tcp'" label="目标地址"
          ><el-input
            v-model="profileEditor.probe!.addr"
            aria-label="探测目标地址"
            placeholder="example.com:443" /></el-form-item
        ><template v-else
          ><el-form-item label="目标 URL"
            ><el-input
              v-model="profileEditor.probe!.url"
              aria-label="探测目标 URL"
              placeholder="https://example.com/health" /></el-form-item
          ><el-form-item label="期望状态码"
            ><el-input-number
              v-model="profileEditor.probe!.expectedStatus"
              :min="100"
              :max="599"
              :value-on-clear="200" /></el-form-item
        ></template>
        <div class="form-pair">
          <el-form-item label="探测间隔（秒）"
            ><el-input-number
              v-model="profileEditor.probe!.interval"
              :min="1"
              :max="86400"
              controls-position="right" /></el-form-item
          ><el-form-item label="探测超时（秒）"
            ><el-input-number
              v-model="profileEditor.probe!.timeout"
              :min="0.1"
              :max="60"
              :step="0.1"
              controls-position="right"
          /></el-form-item>
        </div>
        <p class="hint">
          新建代理池后，请添加至少一个节点再应用。修改 profile 名称后，上游认证密码需重新填写。
        </p></template
      ></el-form
    ><template #footer
      ><el-button @click="profileOpen = false">取消</el-button
      ><el-button type="primary" @click="saveProfile">保存到草稿</el-button></template
    >
  </el-drawer>
</template>
