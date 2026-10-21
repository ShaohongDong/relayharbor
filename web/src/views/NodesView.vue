<script setup lang="ts">
import Icon from '../Icon.vue'
import { healthStates, useConsole } from '../console'

const {
  draft,
  status,
  disconnected,
  dirty,
  pools,
  rows,
  visibleRows,
  search,
  filterPool,
  filterHealth,
  page,
  profilesOpen,
  sortChanged,
  changedFilter,
  probe,
  editNode,
  removeNode,
} = useConsole()

function rowKey(row: { profile: string; id: string }) {
  return `${row.profile}/${row.id}`
}
const tone = (health: string) =>
  health === '正常' ? 'ok' : health === '故障' ? 'bad' : health === '连接中断' ? 'warn' : 'idle'
</script>

<template>
  <section class="card card-flush">
    <div class="toolbar">
      <el-input
        v-model="search"
        aria-label="搜索节点"
        placeholder="搜索节点 ID、地址、协议…"
        clearable
        class="search"
        @input="changedFilter"
        ><template #prefix><Icon name="search" :size="15" /></template
      ></el-input>
      <el-select
        v-model="filterPool"
        aria-label="代理池筛选"
        placeholder="全部代理池"
        clearable
        @change="changedFilter"
        ><el-option v-for="p in draft!.profiles" :key="p.name" :label="p.name" :value="p.name"
      /></el-select>
      <el-select
        v-model="filterHealth"
        aria-label="健康状态筛选"
        placeholder="全部健康状态"
        clearable
        @change="changedFilter"
        ><el-option v-for="h in healthStates" :key="h" :label="h" :value="h"
      /></el-select>
      <span class="spacer" />
      <el-button @click="profilesOpen = true">管理代理池</el-button
      ><el-button type="primary" :disabled="!pools.length" @click="editNode()"
        ><Icon name="plus" :size="15" /><span>添加节点</span></el-button
      >
    </div>
    <div class="table-meta">
      <span>{{ draft!.profiles.length }} 个 profile · {{ rows.length }} 个节点</span>
      <span
        v-for="p in draft!.profiles.filter((p) => p.type === 'direct')"
        :key="p.name"
        class="badge"
        >{{ p.name }} · 直连，无上游节点</span
      >
      <el-button
        v-if="filterPool && pools.some((p) => p.name === filterPool)"
        link
        type="primary"
        :disabled="!status?.active || dirty || disconnected"
        @click="probe(filterPool)"
        >探测所选池</el-button
      >
    </div>
    <el-table
      :data="visibleRows"
      :row-key="rowKey"
      @sort-change="sortChanged"
      empty-text="没有匹配的节点。可先创建代理池，再添加节点。"
      class="node-table"
    >
      <el-table-column prop="profile" label="所属池" min-width="120" sortable="custom" />
      <el-table-column prop="id" label="节点 ID" min-width="140" sortable="custom"
        ><template #default="{ row }"
          ><span class="strong">{{ row.id }}</span></template
        ></el-table-column
      >
      <el-table-column prop="type" label="类型" width="100"
        ><template #default="{ row }"
          ><span class="badge mono">{{ row.type === 'http' ? 'HTTP' : 'SOCKS5' }}</span></template
        ></el-table-column
      >
      <el-table-column prop="addr" label="地址" min-width="190" show-overflow-tooltip
        ><template #default="{ row }"
          ><span class="mono">{{ row.addr }}</span></template
        ></el-table-column
      >
      <el-table-column prop="health" label="健康" width="110"
        ><template #default="{ row }"
          ><span class="health" :class="`tone-${tone(row.health)}`"
            ><span class="status-dot" />{{ row.health }}</span
          ></template
        ></el-table-column
      >
      <el-table-column label="延迟" width="100"
        ><template #default="{ row }"
          ><span class="mono">{{
            row.state?.latencyMs == null || disconnected || !status?.active
              ? '—'
              : row.state.latencyMs.toFixed(1) + ' ms'
          }}</span></template
        ></el-table-column
      >
      <el-table-column label="最近探测" min-width="110"
        ><template #default="{ row }">{{
          row.state?.lastProbe
            ? new Date(row.state.lastProbe * 1000).toLocaleTimeString('zh-CN', {
                hour12: false,
              })
            : '—'
        }}</template></el-table-column
      >
      <el-table-column label="失败次数" width="90"
        ><template #default="{ row }">{{ row.state?.failures ?? '—' }}</template></el-table-column
      >
      <el-table-column label="操作" width="130" fixed="right"
        ><template #default="{ row }"
          ><el-button link type="primary" @click="editNode(row.profile, row)">编辑</el-button
          ><el-button link type="danger" @click="removeNode(row.profile, row.id)"
            >删除</el-button
          ></template
        ></el-table-column
      >
    </el-table>
    <el-pagination
      v-if="rows.length > 50"
      v-model:current-page="page"
      :page-size="50"
      :total="rows.length"
      layout="total, prev, pager, next"
      class="pagination"
    />
  </section>
  <p class="hint">
    健康节点按新连接轮询；故障切换限于当前池。已有连接保持原路径，全池不可用时返回失败。
  </p>
</template>
