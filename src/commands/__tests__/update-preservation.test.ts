import { beforeEach, afterEach, expect, it, vi } from 'vitest'
const f = vi.hoisted(() => ({ lstat: vi.fn(), readFile: vi.fn(), check: vi.fn(), exec: vi.fn() }))
vi.mock('fs-extra', async (original) => { const actual = await original<{ default: typeof import('fs-extra') }>(); return { default: { ...actual.default, lstat: f.lstat, readFile: f.readFile } } })
vi.mock('node:child_process', () => ({ exec: f.exec }))
vi.mock('../../utils/version', () => ({ checkForUpdates: f.check, compareVersions: vi.fn() }))
vi.mock('../../utils/config', () => ({ readCcgConfig: vi.fn(() => { throw Error('unexpected config read') }), writeCcgConfig: vi.fn() }))
vi.mock('../../utils/migration', () => ({ migrateToV1_4_0: vi.fn(), needsMigration: vi.fn() }))
vi.mock('ora', () => ({ default: () => ({ start: () => ({ stop: vi.fn() }) }) }))
vi.mock('../../i18n', () => ({ i18n: { t: (key: string) => key } }))
import { update } from '../update'
beforeEach(() => {
 vi.clearAllMocks()
 vi.spyOn(console, 'log').mockImplementation(() => {})
 f.check.mockRejectedValue(Error('fixture network boundary'))
 f.exec.mockImplementation(() => { throw Error('unexpected subprocess') })
})
afterEach(() => { vi.restoreAllMocks() })
for (const kind of ['script', 'link', 'dangling', 'unreadable', 'native', 'missing']) {
 it(`update preserves ${kind} boundary before network or child installer`, async () => {
  f.lstat.mockImplementation(async () => {
   if (kind === 'missing' || kind === 'unreadable') throw Object.assign(Error('fixture'), { code: kind === 'missing' ? 'ENOENT' : 'EACCES' })
   return { isSymbolicLink: () => kind === 'link' || kind === 'dangling', isFile: () => true }
  })
  f.readFile.mockResolvedValue(Buffer.from(kind === 'script' ? '#!/bin/sh' : '\x7fELF'))
  await update()
  const blocked = !['native', 'missing'].includes(kind)
  expect(f.check).toHaveBeenCalledTimes(blocked ? 0 : 1)
  expect(f.exec).not.toHaveBeenCalled()
  if (blocked) expect(vi.mocked(console.log).mock.calls.flat().join(' ')).toContain('Update not performed')
 })
}
