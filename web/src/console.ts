import { computed, inject, onBeforeUnmount, onMounted, provide, ref, type InjectionKey } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { APIError, request } from './api'
import {
  clone,
  needsRestart,
  payload,
  redacted,
  validateDraft,
  type Config,
  type Node,
  type Profile,
  type Status,
  type LogResult,
} from './model'

export const views = [
  'overview',
  'nodes',
  'listener',
  'subscriptions',
  'https',
  'operations',
  'setup',
] as const
export type View = (typeof views)[number]
export const healthStates = ['正常', '故障', '未知', '服务停止', '连接中断'] as const

function viewFromHash(): View {
  const v = location.hash.replace(/^#\/?/, '')
  return (views as readonly string[]).includes(v) ? (v as View) : 'overview'
}

export function createConsole() {
  const token = ref(''),
    busy = ref(false),
    refreshing = ref(false)
  const view = ref<View>(viewFromHash())
  const error = ref(''),
    disconnected = ref(false),
    conflict = ref(false)
  const base = ref<Config>(),
    draft = ref<Config>(),
    status = ref<Status>(),
    revision = ref('')
  const search = ref(''),
    filterPool = ref(''),
    filterHealth = ref(''),
    page = ref(1)
  const sortKey = ref(''),
    sortOrder = ref('')
  const profilesOpen = ref(false),
    nodeOpen = ref(false),
    profileOpen = ref(false),
    previewOpen = ref(false)
  const nodeEditor = ref<Node>({ id: '', type: 'socks5', addr: '' }),
    nodePool = ref(''),
    nodeOrigin = ref<{ pool: string; id: string }>()
  const profileEditor = ref<Profile>({ name: '', type: 'direct' }),
    profileOrigin = ref('')
  const logs = ref<LogResult>(),
    logLimit = ref(100)
  const dirty = computed(
    () =>
      !!draft.value && !!base.value && JSON.stringify(draft.value) !== JSON.stringify(base.value),
  )
  const restart = computed(
    () => !!draft.value && !!base.value && needsRestart(draft.value, base.value),
  )
  const pools = computed(() => draft.value?.profiles.filter((p) => p.type === 'pool') ?? [])
  const preview = computed(() =>
    draft.value ? JSON.stringify(redacted(payload(draft.value)), null, 2) : '',
  )
  const changeSummary = computed(() => {
    if (!draft.value || !base.value) return ''
    const count = (c: Config) => c.profiles.reduce((sum, p) => sum + (p.nodes?.length ?? 0), 0)
    return `代理池 / 直连 ${base.value.profiles.length} → ${draft.value.profiles.length} · 节点 ${count(base.value)} → ${count(draft.value)} · 应用后出口 ${draft.value.activeProfile}`
  })
  const oldNodes = computed(
    () =>
      new Map(
        base.value?.profiles.flatMap((p) =>
          (p.nodes ?? []).map((n) => [`${p.name}/${n.id}`, n] as const),
        ),
      ),
  )
  const nodeStates = computed(
    () => new Map(status.value?.nodes?.map((n) => [`${n.profile}/${n.id}`, n] as const)),
  )
  const rows = computed(() =>
    (
      draft.value?.profiles.flatMap((p) =>
        (p.nodes ?? []).map((n) => {
          const old = oldNodes.value.get(`${p.name}/${n.id}`)
          const state =
            old && JSON.stringify(old) === JSON.stringify(n)
              ? nodeStates.value.get(`${p.name}/${n.id}`)
              : undefined
          return {
            ...n,
            profile: p.name,
            state,
            health: disconnected.value
              ? '连接中断'
              : !status.value?.active
                ? '服务停止'
                : state?.healthy === true
                  ? '正常'
                  : state?.healthy === false
                    ? '故障'
                    : '未知',
          }
        }),
      ) ?? []
    )
      .filter(
        (n) =>
          (!filterPool.value || n.profile === filterPool.value) &&
          (!filterHealth.value || n.health === filterHealth.value) &&
          `${n.profile} ${n.id} ${n.type} ${n.addr}`
            .toLowerCase()
            .includes(search.value.toLowerCase()),
      )
      .sort((a, b) => {
        if (!sortOrder.value) return 0
        const av = sortKey.value === 'profile' ? a.profile : a.id,
          bv = sortKey.value === 'profile' ? b.profile : b.id
        return av.localeCompare(bv) * (sortOrder.value === 'ascending' ? 1 : -1)
      }),
  )
  const visibleRows = computed(() => rows.value.slice((page.value - 1) * 50, page.value * 50))
  // Health summary of the applied configuration, per pool and in total.
  const poolHealth = computed(() =>
    (base.value?.profiles ?? []).map((p) => {
      const nodes = p.nodes ?? []
      const states = nodes.map((n) => nodeStates.value.get(`${p.name}/${n.id}`))
      return {
        profile: p,
        total: nodes.length,
        healthy: states.filter((s) => s?.healthy === true).length,
        failed: states.filter((s) => s?.healthy === false).length,
      }
    }),
  )
  const serviceLabel = computed(() =>
    disconnected.value
      ? '连接中断'
      : !status.value?.active
        ? '服务已停止'
        : status.value.ready
          ? status.value.available
            ? '运行中'
            : '当前出口不可用'
          : '正在启动 / 未就绪',
  )
  const serviceTone = computed(() =>
    disconnected.value
      ? 'bad'
      : !status.value?.active
        ? 'idle'
        : status.value.ready && status.value.available
          ? 'ok'
          : 'warn',
  )
  function go(v: View) {
    view.value = v
    if (location.hash !== `#/${v}`) location.hash = `#/${v}`
  }
  function sortChanged(s: { prop: string; order: string }) {
    sortKey.value = s.prop
    sortOrder.value = s.order
    page.value = 1
  }
  function disconnect() {
    token.value = ''
    base.value = undefined
    draft.value = undefined
    status.value = undefined
    revision.value = ''
    logs.value = undefined
    nodeEditor.value = { id: '', type: 'socks5', addr: '' }
    profileEditor.value = { name: '', type: 'direct' }
    nodeOpen.value = false
    profilesOpen.value = false
    profileOpen.value = false
    previewOpen.value = false
    conflict.value = false
    disconnected.value = false
  }
  function showError(e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
    if (e instanceof APIError) {
      if (e.status === 401) disconnect()
      if (e.code === 'revision_conflict') {
        conflict.value = true
        error.value = '配置已被其他客户端修改。草稿已保留，请重新加载后编辑。'
      }
    } else disconnected.value = true
  }
  async function load() {
    // Do not combine responses from different configuration revisions.
    for (let i = 0; i < 3; i++) {
      const c = await request<Config>(token.value, '/config')
      const s = await request<Status>(token.value, '/status')
      if (c.revision !== s.revision) continue
      base.value = c.data
      draft.value = clone(c.data)
      status.value = s.data
      revision.value = c.revision
      conflict.value = false
      disconnected.value = false
      page.value = 1
      return
    }
    throw new Error('配置持续变化，请稍后重新加载')
  }
  async function action(fn: () => Promise<void>, mutation = false) {
    if (busy.value || refreshing.value) return
    busy.value = true
    error.value = ''
    try {
      await fn()
    } catch (e) {
      showError(e)
      if (mutation && !(e instanceof APIError)) {
        error.value = '连接异常，操作结果待确认。请刷新状态确认后再操作。'
        // Only read back; never replay a mutation after a transport failure.
        try {
          const s = await request<Status>(token.value, '/status')
          status.value = s.data
          disconnected.value = false
          if (s.revision !== revision.value) conflict.value = true
        } catch {
          /* retain the original error */
        }
      }
    } finally {
      busy.value = false
    }
  }
  async function login(csrf: string) {
    await action(async () => {
      token.value = csrf
      try {
        await load()
        const setup = await request<{ completed: boolean }>(token.value, '/setup')
        if (!setup.data.completed) go('setup')
        else if (view.value === 'setup') go('overview')
      } catch (e) {
        disconnect()
        throw e
      }
    })
  }
  async function logout() {
    try {
      await request(token.value, '/auth/logout', 'POST', {})
    } finally {
      disconnect()
    }
  }
  async function refresh() {
    if (!token.value || busy.value || refreshing.value || view.value === 'setup') return
    refreshing.value = true
    try {
      const s = await request<Status>(token.value, '/status')
      status.value = s.data
      disconnected.value = false
      if (s.revision !== revision.value) {
        if (dirty.value || profileOpen.value || nodeOpen.value) conflict.value = true
        else await load()
      }
    } catch (e) {
      showError(e)
    } finally {
      refreshing.value = false
    }
  }
  async function confirm(message: string, title = '确认操作') {
    try {
      await ElMessageBox.confirm(message, title, {
        confirmButtonText: '确认',
        cancelButtonText: '返回',
        type: 'warning',
        closeOnClickModal: false,
      })
      return true
    } catch {
      return false
    }
  }
  async function ensureClean() {
    if (!dirty.value) return true
    if (
      !(await confirm(
        '存在未应用的配置修改。放弃草稿后才能继续；也可以返回页面先应用配置。',
        '未应用修改',
      ))
    )
      return false
    draft.value = clone(base.value!)
    return true
  }
  async function reload() {
    if (await ensureClean()) await action(load)
  }
  async function apply() {
    await action(async () => {
      const problem = validateDraft(draft.value!, base.value!)
      if (problem) throw new APIError(400, 'invalid_draft', problem)
      const candidate = payload(draft.value!)
      await request(token.value, '/config/validate', 'POST', candidate)
      if (
        restart.value &&
        !(await confirm(
          '入口地址、账号或 UDP 设置发生变化，需要重启转发服务；已有连接会中断。是否应用？',
        ))
      )
        return
      await request(token.value, '/config', 'PUT', candidate, revision.value)
      // Immediately forget newly entered passwords after acknowledged success.
      draft.value = clone(redacted(draft.value!))
      base.value = clone(draft.value)
      await load()
      previewOpen.value = false
      ElMessage.success('配置已应用')
    }, true)
  }
  async function useProfile(name: string) {
    if (name === status.value?.activeProfile || !(await ensureClean())) return
    await action(async () => {
      await request(token.value, '/active-profile', 'PUT', { name }, revision.value)
      await load()
      ElMessage.success('出口已切换，已有连接继续使用原路径')
    }, true)
  }
  async function probe(name: string) {
    if (!(await ensureClean())) return
    await action(async () => {
      await request(token.value, `/profiles/${encodeURIComponent(name)}/probe`, 'POST', {})
      const s = await request<Status>(token.value, '/status')
      status.value = s.data
      ElMessage.success('探测完成，故障节点仍遵循冷却时间')
    })
  }
  async function service(verb: 'start' | 'stop' | 'restart' | 'rollback') {
    if (!(await ensureClean())) return
    const labels = { start: '启动', stop: '停止', restart: '重启', rollback: '回滚' }
    const target = status.value?.rollbackTarget
    const message =
      verb === 'rollback'
        ? `恢复上一份配置与 GOST ${target?.version ?? ''}，出口 ${target?.activeProfile ?? ''}，${target?.active ? '运行' : '停止'} / ${target?.enabled ? '开机启动' : '不开机启动'}。可能重启服务并中断已有连接。`
        : `${labels[verb]}转发服务。${verb === 'start' ? '等待入口就绪后返回。' : '已有连接会中断。'}管理页面仍可继续使用。`
    if (verb !== 'start' && !(await confirm(message, `${labels[verb]}服务`))) return
    await action(async () => {
      await request(
        token.value,
        verb === 'rollback' ? '/rollback' : `/service/${verb}`,
        'POST',
        {},
        revision.value,
      )
      await load()
      ElMessage.success(`${labels[verb]}完成`)
    }, true)
  }
  async function readLogs() {
    await action(async () => {
      logs.value = (await request<LogResult>(token.value, `/logs?limit=${logLimit.value}`)).data
    })
  }
  function editNode(pool?: string, node?: Node) {
    nodeOrigin.value = node && pool ? { pool, id: node.id } : undefined
    nodePool.value = pool ?? (filterPool.value || pools.value[0]?.name) ?? ''
    if (!pools.value.some((p) => p.name === nodePool.value))
      nodePool.value = pools.value[0]?.name ?? ''
    nodeEditor.value = node
      ? clone({
          id: node.id,
          type: node.type,
          addr: node.addr,
          ...(node.auth !== undefined ? { auth: node.auth } : {}),
        })
      : { id: '', type: 'socks5', addr: '' }
    nodeOpen.value = true
  }
  function saveNode() {
    const p = draft.value!.profiles.find((v) => v.name === nodePool.value && v.type === 'pool')
    if (!p || !nodeEditor.value.id || !nodeEditor.value.addr) {
      ElMessage.error('请选择代理池并填写节点 ID、地址')
      return
    }
    if (
      p.nodes?.some(
        (n) =>
          n.id === nodeEditor.value.id &&
          !(nodeOrigin.value?.pool === p.name && nodeOrigin.value.id === n.id),
      )
    ) {
      ElMessage.error('池内节点 ID 已存在')
      return
    }
    if (nodeOrigin.value) {
      const old = draft.value!.profiles.find((v) => v.name === nodeOrigin.value!.pool)
      if (old?.nodes) old.nodes = old.nodes.filter((n) => n.id !== nodeOrigin.value!.id)
    }
    p.nodes ??= []
    p.nodes.push(clone(nodeEditor.value))
    nodeOpen.value = false
    page.value = 1
    nodeEditor.value = { id: '', type: 'socks5', addr: '' }
  }
  async function removeNode(pool: string, id: string) {
    if (!(await confirm(`从草稿中删除 ${pool}/${id}？应用配置后生效。`))) return
    const p = draft.value!.profiles.find((p) => p.name === pool)!
    p.nodes = p.nodes?.filter((n) => n.id !== id)
    page.value = 1
  }
  function editProfile(p?: Profile) {
    profileOrigin.value = p?.name ?? ''
    profileEditor.value = p
      ? clone(p)
      : {
          name: '',
          type: 'pool',
          nodes: [],
          probe: { type: 'tcp', addr: '', interval: 30, timeout: 5 },
        }
    profileOpen.value = true
  }
  function profileTypeChanged() {
    if (profileEditor.value.type === 'pool') {
      profileEditor.value.nodes ??= []
      profileEditor.value.probe ??= { type: 'tcp', addr: '', interval: 30, timeout: 5 }
    }
  }
  async function saveProfile() {
    const p = clone(profileEditor.value)
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(p.name)) {
      ElMessage.error('请填写有效 profile 名称')
      return
    }
    if (draft.value!.profiles.some((v) => v.name === p.name && v.name !== profileOrigin.value)) {
      ElMessage.error('profile 名称已存在')
      return
    }
    if (p.type === 'direct') {
      if (p.nodes?.length && !(await confirm('改为直连将移除草稿中的池节点与探测设置。'))) return
      delete p.nodes
      delete p.probe
    }
    const i = draft.value!.profiles.findIndex((v) => v.name === profileOrigin.value)
    if (i >= 0) draft.value!.profiles.splice(i, 1, p)
    else draft.value!.profiles.push(p)
    if (draft.value!.activeProfile === profileOrigin.value) draft.value!.activeProfile = p.name
    profileOpen.value = false
    page.value = 1
  }
  async function removeProfile(name: string) {
    if (draft.value!.activeProfile === name) {
      ElMessage.error('请先选择另一个“应用后出口”，再删除此 profile')
      return
    }
    if (!(await confirm(`从草稿中删除 ${name} 及其所有节点？`))) return
    draft.value!.profiles = draft.value!.profiles.filter((p) => p.name !== name)
    if (filterPool.value === name) filterPool.value = ''
    page.value = 1
  }
  function setNodeAuth(enabled: string | number | boolean) {
    nodeEditor.value.auth = enabled ? { username: '', password: '' } : null
  }
  function changedFilter() {
    page.value = 1
  }
  function showPool(name: string) {
    filterPool.value = name
    page.value = 1
    go('nodes')
  }
  function beforeUnload(e: BeforeUnloadEvent) {
    if (dirty.value || busy.value) {
      e.preventDefault()
      e.returnValue = ''
    }
  }
  function hashChanged() {
    view.value = viewFromHash()
  }
  let timer: ReturnType<typeof setInterval>
  onMounted(() => {
    timer = setInterval(() => {
      if (!document.hidden) void refresh()
    }, 5000)
    window.addEventListener('beforeunload', beforeUnload)
    window.addEventListener('hashchange', hashChanged)
  })
  onBeforeUnmount(() => {
    clearInterval(timer)
    window.removeEventListener('beforeunload', beforeUnload)
    window.removeEventListener('hashchange', hashChanged)
  })

  return {
    token,
    busy,
    refreshing,
    view,
    error,
    disconnected,
    conflict,
    base,
    draft,
    status,
    revision,
    search,
    filterPool,
    filterHealth,
    page,
    profilesOpen,
    nodeOpen,
    profileOpen,
    previewOpen,
    nodeEditor,
    nodePool,
    nodeOrigin,
    profileEditor,
    profileOrigin,
    logs,
    logLimit,
    dirty,
    restart,
    pools,
    preview,
    changeSummary,
    rows,
    visibleRows,
    poolHealth,
    serviceLabel,
    serviceTone,
    go,
    sortChanged,
    showError,
    login,
    logout,
    refresh,
    reload,
    apply,
    useProfile,
    probe,
    service,
    readLogs,
    editNode,
    saveNode,
    removeNode,
    editProfile,
    profileTypeChanged,
    saveProfile,
    removeProfile,
    setNodeAuth,
    changedFilter,
    showPool,
  }
}

export type ConsoleState = ReturnType<typeof createConsole>
const key: InjectionKey<ConsoleState> = Symbol('console')

export function provideConsole() {
  const state = createConsole()
  provide(key, state)
  return state
}

export function useConsole() {
  const state = inject(key)
  if (!state) throw new Error('console state is not provided')
  return state
}
