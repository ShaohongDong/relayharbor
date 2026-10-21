<script setup lang="ts">
import { useConsole } from '../console'

const { previewOpen, preview, changeSummary, restart } = useConsole()
</script>

<template>
  <el-drawer v-model="previewOpen" title="预览变更" size="min(720px, 100vw)">
    <div class="preview-head">
      <p>{{ changeSummary }}</p>
      <el-tag :type="restart ? 'warning' : 'success'">{{
        restart ? '应用时需要重启' : '可热更新'
      }}</el-tag>
    </div>
    <p class="hint">
      仅显示脱敏预览；密码不会出现在此处。校验为静态检查，实际应用仍可能因运行环境失败。
    </p>
    <pre class="code-block" data-testid="config-preview">{{ preview }}</pre>
  </el-drawer>
</template>
