import { test, expect, type Page } from '@playwright/test'
import type { Config } from '../src/model'
const initial = (): Config => ({
  schemaVersion: 2,
  listener: {
    addr: '127.0.0.1:1080',
    udp: false,
    users: [{ username: 'entry', passwordSet: true }],
  },
  activeProfile: 'office',
  profiles: [
    { name: 'direct', type: 'direct' },
    {
      name: 'office',
      type: 'pool',
      nodes: [
        {
          id: 'http-a',
          type: 'http',
          addr: 'proxy-a:8080',
          auth: { username: 'upstream', passwordSet: true },
        },
        { id: 'socks-b', type: 'socks5', addr: 'proxy-b:1080' },
      ],
      probe: { type: 'tcp', addr: 'target:443', interval: 30, timeout: 5 },
    },
  ],
})
test('AnyTLS uses its own revision and preserves an open user editor', async ({ page }) => {
  await setup(page)
  let revision = 'a1'
  const calls: { path: string; revision?: string }[] = []
  await page.route('**/api/v1/anytls**', async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname
    if (req.method() === 'GET') {
      await route.fulfill({
        json: {
          data: {
            configured: true,
            enabled: true,
            domain: 'panel.example.com',
            version: '1.14.3',
            users: [
              { id: 'phone', name: 'Phone', port: 8443, enabled: true, active: true, profile: '' },
            ],
          },
          revision,
        },
      })
      return
    }
    calls.push({ path, revision: req.headers()['if-match'] })
    if (req.headers()['if-match'] !== `"${revision}"`) {
      await route.fulfill({
        status: 409,
        json: { error: { code: 'revision_conflict', message: 'AnyTLS 配置已变化' } },
      })
      return
    }
    if (path.endsWith('/credentials')) {
      await route.fulfill({
        json: {
          data: {
            password: 'device-password',
            url: 'https://panel.example.com/sub/device-token/clash.yaml',
            provider: 'https://panel.example.com/sub/device-token/proxies.yaml',
          },
          revision,
        },
      })
      return
    }
    revision = 'a2'
    await route.fulfill({ json: { data: { state: 'applied' }, revision } })
  })
  await page.getByRole('link', { name: '客户端订阅' }).click()
  await expect(page.getByText('Phone', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '订阅与凭据', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(calls.at(-1)?.revision).toBe('"a1"')
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain('device-token')
  await page.getByRole('dialog').getByRole('button', { name: '关闭此对话框' }).click()
  await page.getByRole('button', { name: '新增用户 / 设备' }).click()
  await page.getByLabel('用户 ID', { exact: true }).fill('tablet')
  await page.getByLabel('显示名称', { exact: true }).fill('Unsaved Tablet')
  revision = 'external-change'
  await page.waitForTimeout(5500)
  await expect(page.getByLabel('显示名称', { exact: true })).toHaveValue('Unsaved Tablet')
  await page.getByRole('button', { name: '保存用户', exact: true }).click()
  expect(calls.at(-1)?.revision).toBe('"a1"')
  await expect(page.getByRole('button', { name: '保存用户', exact: true })).toBeDisabled()
  await expect(page.getByLabel('显示名称', { exact: true })).toHaveValue('Unsaved Tablet')
})
async function setup(page: Page, config = initial()) {
  let revision = 'r1',
    active = true,
    changed = false,
    reject = false
  const calls: { path: string; body: any; headers: Record<string, string> }[] = []
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname.slice(7)
    if (path === '/auth/status') {
      await route.fulfill({ json: { data: { initialized: true }, revision: '' } })
      return
    }
    if (path === '/auth/session') {
      await route.fulfill({
        status: 401,
        json: { error: { code: 'unauthorized', message: '登录' } },
      })
      return
    }
    if (path === '/auth/login') {
      await route.fulfill({ json: { data: { csrf: 'test-token' }, revision: '' } })
      return
    }
    if (path === '/auth/logout') {
      await route.fulfill({ json: { data: {}, revision: '' } })
      return
    }
    if (path === '/setup') {
      await route.fulfill({ json: { data: { completed: true }, revision: 'r1' } })
      return
    }
    const body = request.postDataJSON()
    calls.push({ path, body, headers: request.headers() })
    if (reject && request.method() === 'PUT') {
      await route.fulfill({
        status: 409,
        json: { error: { code: 'revision_conflict', message: '配置已变化' } },
      })
      return
    }
    if (path === '/config' && request.method() === 'PUT') {
      config = body
      revision = 'r2'
      changed = true
    }
    if (path === '/active-profile') {
      config.activeProfile = body.name
      revision = 'r2'
    }
    if (path === '/service/stop') active = false
    if (path === '/service/start' || path === '/service/restart') active = true
    const status = {
      active,
      ready: active,
      enabled: true,
      available: active,
      activeProfile: config.activeProfile,
      version: '3.3.0',
      arch: 'amd64',
      connections: 2,
      rollbackAvailable: changed,
      nodes: config.profiles.flatMap((p) =>
        'nodes' in p
          ? (p.nodes ?? []).map((n, i) => ({
              profile: p.name,
              id: n.id,
              healthy: i === 0,
              latencyMs: i === 0 ? 12 : null,
              lastProbe: 1700000000,
              failures: i,
            }))
          : [],
      ),
    }
    await route.fulfill({
      json: {
        data:
          path === '/config' && request.method() === 'GET'
            ? config
            : path === '/logs'
              ? {
                  entries: [
                    { time: 'now', unit: 'gost.service', message: '<script>literal text</script>' },
                  ],
                  truncated: false,
                }
              : path === '/config/validate'
                ? { valid: true }
                : path === '/profiles'
                  ? config.profiles.map((p) => ({ name: p.name, type: p.type }))
                  : status,
        revision,
      },
    })
  })
  await page.goto('/')
  await page.getByLabel('管理员密码', { exact: true }).fill('test-token')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByTestId('stat-health')).toBeVisible()
  await page.getByRole('link', { name: '代理池与节点' }).click()
  await expect(page.getByRole('button', { name: '管理代理池' })).toBeVisible()
  return {
    calls,
    rejectWrites: () => {
      reject = true
    },
  }
}
test('table filtering, editing, redaction and quoted revision', async ({ page }) => {
  const { calls } = await setup(page)
  await page.getByLabel('搜索节点').fill('http-a')
  await expect(page.locator('.node-table .el-table__body tr')).toHaveCount(1)
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  await page.getByLabel('上游地址', { exact: true }).fill('changed-proxy:8080')
  await page.getByLabel('上游密码', { exact: true }).fill('new-upstream-secret')
  await page.getByRole('button', { name: '保存到草稿' }).click()
  await expect(page.getByText('有未应用修改', { exact: true })).toBeVisible()
  // A periodic status refresh must not overwrite the draft.
  await page.getByRole('button', { name: '刷新状态' }).click()
  await expect(page.getByText('changed-proxy:8080', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '预览变更' }).click()
  await expect(page.getByTestId('config-preview')).toContainText('changed-proxy:8080')
  await expect(page.getByTestId('config-preview')).not.toContainText('new-upstream-secret')
  await page
    .getByRole('dialog', { name: '预览变更' })
    .getByRole('button', { name: '关闭此对话框' })
    .click()
  await page.getByRole('button', { name: '校验并应用' }).click()
  await expect(page.getByText('配置已应用', { exact: true })).toBeVisible()
  const write = calls.find((c) => c.path === '/config' && c.body?.profiles)
  expect(write?.headers['if-match']).toBe('"r1"')
  expect(write?.body.profiles[1].nodes[1].auth.password).toBe('new-upstream-secret')
  expect(JSON.stringify(write?.body)).not.toContain('passwordSet')
  expect(JSON.stringify(write?.body)).not.toMatch(/"state"|"health"|"profile"/)
  expect(
    await page.evaluate(
      () => Object.keys(localStorage).length + Object.keys(sessionStorage).length,
    ),
  ).toBe(0)
  await page.getByRole('button', { name: '断开认证' }).click()
  await expect(page.getByRole('button', { name: '登录控制台' })).toBeVisible()
})
test('revision conflicts preserve draft and disable another write', async ({ page }) => {
  const state = await setup(page)
  state.rejectWrites()
  await page.getByRole('button', { name: '添加节点' }).click()
  await page.getByLabel('节点 ID', { exact: true }).fill('draft-node')
  await page.getByLabel('上游地址', { exact: true }).fill('new-proxy:1080')
  await page.getByRole('button', { name: '保存到草稿' }).click()
  await page.getByRole('button', { name: '校验并应用' }).click()
  await expect(
    page.getByText('配置已被其他客户端修改。草稿已保留，请重新加载后编辑。'),
  ).toBeVisible()
  await expect(page.getByText('draft-node', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '校验并应用' })).toBeDisabled()
})
test('service controls, plain text logs and narrow layout', async ({ page }) => {
  await setup(page)
  await page.getByRole('link', { name: '服务运维' }).click()
  await page.getByRole('button', { name: '停止服务', exact: true }).click()
  await page.getByRole('button', { name: '确认', exact: true }).click()
  await expect(page.getByRole('button', { name: '启动服务', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: '启动服务', exact: true }).click()
  await expect(page.getByRole('button', { name: '停止服务', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: '读取日志' }).click()
  await expect(page.getByTestId('logs')).toContainText('<script>literal text</script>')
  expect(await page.locator('script').count()).toBeLessThan(10)
  await page.setViewportSize({ width: 390, height: 844 })
  // On phones the navigation rail becomes a bottom tab bar.
  await page.getByRole('link', { name: '代理池与节点' }).click()
  await expect(page.getByRole('link', { name: '代理池与节点' })).toHaveAttribute(
    'aria-current',
    'page',
  )
  await expect(page.getByRole('button', { name: '添加节点' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
  await page.screenshot({ animations: 'disabled', path: '../test-results/console-mobile.png' })
})

test('HTTPS setup hint, background renewal and failure states', async ({ page }) => {
  await setup(page)
  let tls: any = { configured: false, enabled: false }
  const mutations: any[] = []
  await page.route('**/api/v1/tls**', async (route) => {
    if (route.request().method() === 'POST') {
      mutations.push({ headers: route.request().headers(), body: route.request().postDataJSON() })
      tls = { ...tls, running: true, operation: 'renew' }
    }
    await route.fulfill({ json: { data: tls, revision: 'r1' } })
  })
  await page.getByRole('link', { name: 'HTTPS 证书' }).click()
  const panel = page.getByTestId('tls-panel')
  await expect(panel).toContainText('尚未配置 HTTPS')
  tls = {
    configured: true,
    enabled: true,
    domain: 'panel.example.com',
    issuer: 'Test CA',
    notAfter: '2030-01-01T00:00:00Z',
    nginxActive: true,
    timerActive: true,
    timerEnabled: true,
    running: false,
  }
  await panel.getByRole('button', { name: '刷新 HTTPS 状态' }).click()
  await expect(panel).toContainText('panel.example.com')
  await panel.getByRole('button', { name: '检查续期', exact: true }).click()
  await expect(panel).toContainText('正在检查证书续期')
  await expect(panel.getByRole('button', { name: '检查续期', exact: true })).toBeDisabled()
  expect(mutations).toHaveLength(1)
  expect(mutations[0].headers['if-match']).toBe('"r1"')
  expect(mutations[0].headers['x-csrf-token']).toBe('test-token')
  expect(mutations[0].headers.authorization).toBeUndefined()
  expect(mutations[0].body).toEqual({})
  tls = {
    ...tls,
    running: false,
    lastResult: {
      operation: 'renew',
      finishedAt: '2026-10-20T10:00:00Z',
      acquired: false,
      deployed: false,
      error: '证书获取失败，请检查 DNS',
    },
  }
  await panel.getByRole('button', { name: '刷新 HTTPS 状态' }).click()
  await expect(panel).toContainText('证书获取失败，请检查 DNS')
  tls = { ...tls, lastResult: undefined, timerActive: false }
  await panel.getByRole('button', { name: '刷新 HTTPS 状态' }).click()
  await expect(panel).toContainText('自动续期定时任务未运行')
  tls = { ...tls, timerActive: true, expiring: true }
  await panel.getByRole('button', { name: '刷新 HTTPS 状态' }).click()
  await expect(panel).toContainText('证书将在 14 天内到期或已过期')
})

test('first visit initializes password, restores setup and can finish in basic mode', async ({
  page,
}) => {
  await setup(page)
  let initialized = false,
    completed = false
  const passwords: unknown[] = []
  await page.route('**/api/v1/auth/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/status')) {
      await route.fulfill({ json: { data: { initialized }, revision: '' } })
      return
    }
    if (path.endsWith('/initialize')) {
      passwords.push(route.request().postDataJSON())
      initialized = true
      await route.fulfill({ json: { data: { csrf: 'test-token' }, revision: '' } })
      return
    }
    await route.fulfill({ json: { data: { csrf: 'test-token' }, revision: '' } })
  })
  await page.route('**/api/v1/setup**', async (route) => {
    if (route.request().method() === 'POST') completed = true
    await route.fulfill({
      json: {
        data: {
          completed,
          url: 'https://127.0.0.1:18443',
          temporaryURL: 'https://127.0.0.1:18443',
        },
        revision: 'r1',
      },
    })
  })
  await page.route('**/api/v1/tls', (route) =>
    route.fulfill({ json: { data: { enabled: false }, revision: 'r1' } }),
  )
  await page.route('**/api/v1/anytls', (route) =>
    route.fulfill({ json: { data: { configured: false }, revision: 'unconfigured' } }),
  )
  await page.reload()
  await page.getByLabel('一次性初始化码', { exact: true }).fill('one-time-code')
  await page.getByLabel('管理员密码', { exact: true }).fill('new administrator password')
  await page.getByLabel('确认密码', { exact: true }).fill('new administrator password')
  await page.getByRole('button', { name: '设置密码并继续' }).click()
  await expect(page.getByRole('region', { name: '部署向导' })).toBeVisible()
  await expect(page.getByRole('button', { name: '初始化 AnyTLS', exact: true })).toBeDisabled()
  await page.reload()
  await expect(page.getByRole('region', { name: '部署向导' })).toBeVisible()
  expect(passwords).toHaveLength(1)
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain(
    'new administrator password',
  )
  // Before completion the wizard is the only reachable page.
  const wizard = page.getByRole('region', { name: '部署向导' })
  const nav = page.getByRole('navigation', { name: '主导航' })
  await expect(nav.getByRole('link')).toHaveCount(1)
  await expect(nav.getByRole('link', { name: '部署设置' })).toBeVisible()
  await page.evaluate(() => (location.hash = '#/overview'))
  await expect(page).toHaveURL(/#\/setup$/)
  await expect(page.getByRole('region', { name: '部署向导' })).toBeVisible()
  await wizard.getByRole('button', { name: '进入控制台', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '返回' }).click()
  await expect(page.getByRole('region', { name: '部署向导' })).toBeVisible()
  expect(completed).toBe(false)
  await wizard.getByRole('button', { name: '进入控制台', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '进入控制台' }).click()
  await expect(page.getByRole('region', { name: '部署向导' })).not.toBeVisible()
  await expect(page).toHaveURL(/#\/overview$/)
  await expect(nav.getByRole('link', { name: '部署设置' })).toHaveCount(0)
  await page.reload()
  await expect(page.getByTestId('stat-health')).toBeVisible()
  await expect(page.getByRole('region', { name: '部署向导' })).not.toBeVisible()
  // After completion the wizard has no entry, even by URL.
  await page.evaluate(() => (location.hash = '#/setup'))
  await expect(page).toHaveURL(/#\/overview$/)
  await expect(page.getByRole('region', { name: '部署向导' })).not.toBeVisible()
  await expect(nav.getByRole('link', { name: '部署设置' })).toHaveCount(0)
  await page.goto('/#/setup')
  await expect(page.getByTestId('stat-health')).toBeVisible()
  await expect(page).toHaveURL(/#\/overview$/)
  await nav.getByRole('link', { name: 'HTTPS 证书' }).click()
  await expect(page.locator('a[href="#/setup"]')).toHaveCount(0)
  await nav.getByRole('link', { name: '服务运维' }).click()
  await expect(page.getByText('修改管理员密码')).toBeVisible()
})

test('AnyTLS can be initialized from its page after setup is completed', async ({ page }) => {
  await setup(page)
  let configured = false
  const posts: { revision?: string; key?: string }[] = []
  await page.route('**/api/v1/setup**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const job = path.includes('/setup/jobs/')
      ? { id: 'j1', kind: 'anytls', status: 'succeeded', step: '部署完成' }
      : undefined
    if (job) configured = true
    await route.fulfill({ json: { data: job ?? { completed: true }, revision: 'r1' } })
  })
  await page.route('**/api/v1/anytls/setup', async (route) => {
    posts.push({
      revision: route.request().headers()['if-match'],
      key: route.request().headers()['idempotency-key'],
    })
    await route.fulfill({
      status: 202,
      json: {
        data: { id: 'j1', kind: 'anytls', status: 'queued', step: '等待后台部署' },
        revision: '',
      },
    })
  })
  await page.route('**/api/v1/tls', (route) =>
    route.fulfill({ json: { data: { enabled: true }, revision: 'r1' } }),
  )
  await page.route('**/api/v1/anytls', (route) =>
    route.fulfill({
      json: {
        data: { configured, enabled: configured, users: [] },
        revision: configured ? 'a1' : 'unconfigured',
      },
    }),
  )
  await page.getByRole('link', { name: '客户端订阅' }).click()
  await expect(page.getByText('AnyTLS 尚未初始化')).toBeVisible()
  await expect(page.locator('a[href="#/setup"]')).toHaveCount(0)
  await page.getByRole('button', { name: '初始化 AnyTLS', exact: true }).click()
  await expect(page.getByText('等待后台部署 · queued')).toBeVisible()
  expect(posts).toHaveLength(1)
  expect(posts[0].revision).toBe('"r1"')
  expect(posts[0].key?.length).toBeGreaterThanOrEqual(16)
  await expect(page.getByText('AnyTLS 尚未初始化')).not.toBeVisible({ timeout: 8000 })
})

test('overview summarises health and navigation survives a reload', async ({ page }) => {
  await setup(page)
  await page.getByRole('link', { name: '概览' }).click()
  await expect(page).toHaveURL(/#\/overview$/)
  await expect(page.getByTestId('stat-health')).toHaveText('1 / 2')
  await expect(page.getByTestId('stat-connections')).toHaveText('2')
  await page.getByRole('button', { name: '查看节点' }).click()
  await expect(page).toHaveURL(/#\/nodes$/)
  await expect(page.locator('.node-table .el-table__body tr')).toHaveCount(2)
  await page.getByRole('link', { name: '入口与认证' }).click()
  await expect(page.getByRole('link', { name: '入口与认证' })).toHaveAttribute(
    'aria-current',
    'page',
  )
  await page.reload()
  await page.getByLabel('管理员密码', { exact: true }).fill('test-token')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByLabel('监听地址')).toHaveValue('127.0.0.1:1080')
  await expect(page.getByRole('region', { name: '未应用修改' })).toHaveCount(0)
  await page.getByLabel('监听地址').fill('[::]:1081')
  await expect(page.getByRole('region', { name: '未应用修改' })).toContainText('应用时需要重启服务')
})
test('AnyTLS user egress is pinned through its route operation', async ({ page }) => {
  await setup(page)
  const posts: { path: string; body: any }[] = []
  await page.route('**/api/v1/anytls**', async (route) => {
    const req = route.request()
    if (req.method() === 'POST')
      posts.push({ path: new URL(req.url()).pathname, body: req.postDataJSON() })
    await route.fulfill({
      json: {
        data:
          req.method() === 'GET'
            ? {
                configured: true,
                enabled: true,
                domain: 'panel.example.com',
                users: [
                  {
                    id: 'phone',
                    name: 'Phone',
                    port: 8443,
                    enabled: true,
                    active: true,
                    profile: '',
                  },
                ],
              }
            : { state: 'applied' },
        revision: 'a1',
      },
    })
  })
  await page.getByRole('link', { name: '客户端订阅' }).click()
  await page.getByRole('combobox', { name: '出口 phone', exact: true }).press('Enter')
  await page.getByRole('option', { name: 'office', exact: true }).click()
  await page.getByRole('button', { name: '确认', exact: true }).click()
  await expect
    .poll(() => posts.at(-1))
    .toEqual({
      path: '/api/v1/anytls/users/phone/route',
      body: { profile: 'office' },
    })
})
test('additional entries are drafted with their own users and egress', async ({ page }) => {
  const { calls } = await setup(page)
  await page.getByRole('link', { name: '入口与认证' }).click()
  await page.getByRole('button', { name: '添加附加入口' }).click()
  await page.getByLabel('附加入口名称 1').fill('branch')
  await page.getByLabel('附加入口地址 1').fill('127.0.0.1:1082')
  await page.getByLabel('附加入口 1 用户名 1').fill('branch-user')
  await page.getByLabel('附加入口 1 密码 1').fill('branch-secret')
  await page.getByRole('button', { name: '预览变更' }).click()
  await expect(page.getByTestId('config-preview')).toContainText('branch-user')
  await expect(page.getByTestId('config-preview')).not.toContainText('branch-secret')
  await page
    .getByRole('dialog', { name: '预览变更' })
    .getByRole('button', { name: '关闭此对话框' })
    .click()
  await page.getByRole('button', { name: '校验并应用' }).click()
  await page
    .getByRole('dialog', { name: '确认操作' })
    .getByRole('button', { name: '确认', exact: true })
    .click()
  await expect
    .poll(() => calls.find((c) => c.path === '/config' && c.body?.entries)?.body.entries)
    .toEqual([
      {
        name: 'branch',
        addr: '127.0.0.1:1082',
        udp: false,
        users: [{ username: 'branch-user', password: 'branch-secret' }],
        profile: 'office',
      },
    ])
})

function entryConfig(udp: boolean): Config {
  return {
    ...initial(),
    entries: [
      {
        name: 'branch',
        addr: '127.0.0.1:1082',
        udp,
        users: [{ username: 'branch-user', passwordSet: true }],
        profile: 'direct',
      },
    ],
  }
}

test('an existing entry can disable UDP after selecting a pool and requires restart confirmation', async ({
  page,
}) => {
  const { calls } = await setup(page, entryConfig(true))
  await page.getByRole('link', { name: '入口与认证' }).click()
  const udp = page.getByRole('switch', { name: '附加入口 UDP 1', exact: true })
  const udpControl = page.locator('.el-switch').filter({ has: udp })
  const profile = page.getByRole('combobox', { name: '附加入口出口 1', exact: true })
  const apply = page.getByRole('button', { name: '校验并应用' })
  const confirmation = page.getByRole('dialog', { name: '确认操作' })
  const writes = () => calls.filter((c) => c.path === '/config' && c.body?.entries)
  await expect(udp).toBeChecked()
  await expect(udp).toBeEnabled()
  await profile.press('Enter')
  await page.getByRole('option', { name: 'office', exact: true }).click()
  await expect(udp).toBeChecked()
  await apply.click()
  await expect(page.getByText('branch：UDP 仅支持直连出口', { exact: true })).toBeVisible()
  expect(calls.filter((c) => c.path === '/config/validate')).toHaveLength(0)
  expect(writes()).toHaveLength(0)
  await expect(udp).toBeEnabled()
  await udpControl.click()
  await expect(udp).not.toBeChecked()
  await expect(udp).toBeDisabled()

  await profile.press('Enter')
  await page.getByRole('option', { name: 'direct', exact: true }).click()
  await expect(udp).not.toBeChecked()
  await expect(udp).toBeEnabled()
  await udpControl.click()
  await expect(udp).toBeChecked()
  await udpControl.click()
  await expect(udp).not.toBeChecked()
  await profile.press('Enter')
  await page.getByRole('option', { name: 'office', exact: true }).click()
  await expect(udp).toBeDisabled()

  await apply.click()
  await expect(confirmation).toContainText('已有连接会中断')
  expect(writes()).toHaveLength(0)
  await confirmation.getByRole('button', { name: '返回', exact: true }).click()
  await expect(confirmation).not.toBeVisible()
  expect(writes()).toHaveLength(0)
  await expect(page.locator('.entry-block .el-select')).toContainText('office')
  await expect(udp).not.toBeChecked()
  await expect(page.getByText('有未应用修改', { exact: true })).toBeVisible()

  await apply.click()
  await expect(confirmation).toBeVisible()
  expect(writes()).toHaveLength(0)
  await confirmation.getByRole('button', { name: '确认', exact: true }).click()
  await expect(page.getByText('配置已应用', { exact: true })).toBeVisible()
  expect(writes()).toHaveLength(1)
  expect(writes()[0]?.headers['if-match']).toBe('"r1"')
  const expectedEntries = [
    {
      name: 'branch',
      addr: '127.0.0.1:1082',
      udp: false,
      users: [{ username: 'branch-user' }],
      profile: 'office',
    },
  ]
  expect(writes()[0]?.body.entries).toEqual(expectedEntries)
  expect(calls.find((c) => c.path === '/config/validate')?.body.entries).toEqual(expectedEntries)
})

test('an entry with UDP disabled can switch to a pool without restart confirmation', async ({
  page,
}) => {
  const { calls } = await setup(page, entryConfig(false))
  await page.getByRole('link', { name: '入口与认证' }).click()
  const udp = page.getByRole('switch', { name: '附加入口 UDP 1', exact: true })
  await expect(udp).not.toBeChecked()
  await expect(udp).toBeEnabled()
  await page.getByRole('combobox', { name: '附加入口出口 1', exact: true }).press('Enter')
  await page.getByRole('option', { name: 'office', exact: true }).click()
  await expect(udp).not.toBeChecked()
  await expect(udp).toBeDisabled()
  await page.getByRole('button', { name: '校验并应用' }).click()
  await expect(page.getByText('配置已应用', { exact: true })).toBeVisible()
  await expect(page.getByRole('dialog', { name: '确认操作' })).not.toBeVisible()
  const writes = calls.filter((c) => c.path === '/config' && c.body?.entries)
  expect(writes).toHaveLength(1)
  expect(writes[0]?.body.entries[0]).toMatchObject({ profile: 'office', udp: false })
})
