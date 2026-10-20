import { describe, expect, it } from 'vitest'
import { clone, payload, redacted, needsRestart, validateDraft, type Config } from './model'
const fixture = (): Config => ({
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
          id: 'a',
          type: 'socks5',
          addr: 'localhost:1081',
          auth: { username: 'upstream', passwordSet: true },
        },
      ],
      probe: { type: 'tcp', addr: 'localhost:443', interval: 30, timeout: 5 },
    },
  ],
})
describe('configuration drafts', () => {
  it('preserves omitted secrets without sending display metadata', () => {
    const c = fixture(),
      data = payload(c)
    expect(data.listener.users[0]).toEqual({ username: 'entry' })
    expect(data.profiles[1]!.nodes![0]!.auth).toEqual({ username: 'upstream' })
    expect(c.listener.users[0]!.passwordSet).toBe(true)
    expect(validateDraft(c, c)).toBeNull()
  })
  it('requires new credentials after identity changes or moving pools', () => {
    const base = fixture(),
      c = clone(base)
    c.profiles[1]!.nodes![0]!.id = 'renamed'
    expect(validateDraft(c, base)).toContain('必须填写密码')
    c.profiles[1]!.nodes![0]!.auth!.password = 'new-secret'
    expect(validateDraft(c, base)).toBeNull()
    c.listener.users[0]!.username = 'changed'
    expect(validateDraft(c, base)).toContain('入口身份')
  })
  it('redacts new secrets from preview and retains auth removal', () => {
    const c = fixture()
    c.listener.users[0]!.password = 'entry-secret'
    c.profiles[1]!.nodes![0]!.auth = { username: 'new', password: 'upstream-secret' }
    expect(JSON.stringify(redacted(c))).not.toMatch(/entry-secret|upstream-secret/)
    expect(redacted(c).listener.users[0]!.passwordSet).toBe(true)
    c.profiles[1]!.nodes![0]!.auth = null
    expect(payload(c).profiles[1]!.nodes![0]!.auth).toBeNull()
  })
  it('distinguishes listener restarts from routing changes', () => {
    const base = fixture(),
      c = clone(base)
    c.activeProfile = 'direct'
    expect(needsRestart(c, base)).toBe(false)
    c.listener.udp = true
    expect(needsRestart(c, base)).toBe(true)
  })
  it('blocks invalid active profiles and duplicated or empty pools', () => {
    const base = fixture(),
      c = clone(base)
    c.activeProfile = 'missing'
    expect(validateDraft(c, base)).toContain('有效')
    c.activeProfile = 'office'
    c.profiles[1]!.nodes = []
    expect(validateDraft(c, base)).toContain('至少需要一个节点')
  })
  it('normalizes probe fields and direct profiles', () => {
    const c = fixture()
    c.profiles[1]!.probe!.url = 'stale'
    expect(payload(c).profiles[1]!.probe).not.toHaveProperty('url')
    c.profiles[1]!.type = 'direct'
    expect(payload(c).profiles[1]).toEqual({ name: 'office', type: 'direct' })
  })
})
