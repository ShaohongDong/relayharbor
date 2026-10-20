export interface Auth {
  username: string
  password?: string
  passwordSet?: boolean
}
export interface Node {
  id: string
  type: 'http' | 'socks5'
  addr: string
  auth?: Auth | null
}
export interface Probe {
  type: 'tcp' | 'http'
  addr?: string
  url?: string
  interval: number
  timeout: number
  expectedStatus?: number
}
export interface Profile {
  name: string
  type: 'direct' | 'pool'
  nodes?: Node[]
  probe?: Probe
}
export interface Config {
  schemaVersion: 2
  listener: { addr: string; udp: boolean; users: Auth[] }
  activeProfile: string
  profiles: Profile[]
}
export interface NodeStatus {
  profile: string
  id: string
  healthy: boolean | null
  latencyMs: number | null
  lastProbe: number | null
  failures: number
}
export interface Status {
  active: boolean
  ready?: boolean
  available: boolean
  enabled: boolean
  activeProfile: string
  version: string
  arch: string
  connections?: number
  nodes?: NodeStatus[]
  rollbackAvailable?: boolean
  rollbackTarget?: { version: string; activeProfile: string; active: boolean; enabled: boolean }
}
export interface LogResult {
  entries: { time: string; unit: string; priority: string; message: string }[]
  truncated: boolean
}
export interface TLSStatus {
  configured: boolean
  enabled: boolean
  domain?: string
  url?: string
  issuer?: string
  notBefore?: string
  notAfter?: string
  expiring: boolean
  nginxActive: boolean
  timerEnabled: boolean
  timerActive: boolean
  nextCheck?: string
  operation?: 'renew' | 'reload'
  running: boolean
  warning?: string
  lastResult?: {
    operation: string
    startedAt: string
    finishedAt: string
    acquired: boolean
    deployed: boolean
    error?: string
  }
}
export const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T
export function payload(c: Config): Config {
  const out = clone(c)
  const clean = (a: Auth) => {
    delete a.passwordSet
    if (!a.password) delete a.password
  }
  out.listener.users.forEach(clean)
  out.profiles.forEach((p) => {
    if (p.type === 'direct') {
      delete p.nodes
      delete p.probe
    } else {
      p.nodes?.forEach((n) => {
        if (n.auth) clean(n.auth)
      })
      if (p.probe?.type === 'tcp') {
        delete p.probe.url
        delete p.probe.expectedStatus
      } else if (p.probe) delete p.probe.addr
    }
  })
  return out
}
export function redacted(c: Config): Config {
  const out = clone(c)
  const hide = (a: Auth) => {
    if (a.password || a.passwordSet) a.passwordSet = true
    delete a.password
  }
  out.listener.users.forEach(hide)
  out.profiles.forEach((p) =>
    p.nodes?.forEach((n) => {
      if (n.auth) hide(n.auth)
    }),
  )
  return out
}
export function needsRestart(c: Config, base: Config): boolean {
  return JSON.stringify(payload(c).listener) !== JSON.stringify(payload(base).listener)
}
export function validateDraft(c: Config, base: Config): string | null {
  const ids = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/
  const unique = (xs: string[]) => new Set(xs).size === xs.length
  const authError = (a: Auth, old?: Auth) => {
    if (!a.username) return true
    if (!a.password && (!old || old.username !== a.username || !old.passwordSet)) return true
    return false
  }
  if (!c.listener.addr || !c.listener.users.length) return '请填写入口地址，并保留至少一个入口用户'
  if (!unique(c.listener.users.map((u) => u.username))) return '入口用户名不能重复'
  for (const u of c.listener.users)
    if (
      authError(
        u,
        base.listener.users.find((v) => v.username === u.username),
      )
    )
      return '新增或修改入口身份时必须填写密码'
  if (!c.profiles.length || !c.profiles.some((p) => p.name === c.activeProfile))
    return '请选择应用后使用的有效代理池或直连 profile'
  if (!unique(c.profiles.map((p) => p.name))) return 'profile 名称不能重复'
  for (const p of c.profiles) {
    if (!ids.test(p.name))
      return 'profile 名称须为 1–64 位字母、数字、下划线或连字符，并以字母或数字开头'
    if (p.type === 'direct') continue
    if (!p.nodes?.length || !p.probe) return `${p.name}：代理池至少需要一个节点及探测设置`
    if (!unique(p.nodes.map((n) => n.id))) return `${p.name}：节点 ID 不能重复`
    for (const n of p.nodes) {
      if (!ids.test(n.id) || !n.addr) return `${p.name}：请填写有效节点 ID 和地址`
      const old = base.profiles
        .find((v) => v.name === p.name)
        ?.nodes?.find((v) => v.id === n.id)?.auth
      if (n.auth && authError(n.auth, old ?? undefined))
        return `${p.name}/${n.id}：新增、移动或改名后的认证身份必须填写密码`
    }
  }
  return null
}
