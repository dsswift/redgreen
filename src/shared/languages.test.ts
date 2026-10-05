import { describe, expect, it } from 'vitest'
import { LANGUAGES, listedLanguage, mainLanguage } from './languages.ts'

describe('a repository\'s main language', () => {
  it('is always one of the list', () => {
    expect(listedLanguage('Go')).toBe('Go')
    expect(listedLanguage('HCL')).toBe('Terraform')
    expect(listedLanguage('TSX')).toBe('TypeScript')
    expect(listedLanguage('Mustache')).toBe('Other')
    expect(listedLanguage(null)).toBeNull()
    expect(LANGUAGES.at(-1)).toBe('Other')
  })

  it('is the largest share after shares of one language are added together', () => {
    expect(mainLanguage({ 'C#': 51, Python: 49 })).toBe('C#')
    expect(mainLanguage({ TypeScript: 30, TSX: 25, Go: 45 })).toBe('TypeScript')
    expect(mainLanguage({ HCL: 100 })).toBe('Terraform')
  })

  it('prefers a listed language over build and config files, and does not flap on a tie', () => {
    expect(mainLanguage({ Dockerfile: 70, Go: 30 })).toBe('Go')
    expect(mainLanguage({ Dockerfile: 60, Makefile: 40 })).toBe('Other')
    expect(mainLanguage({ Python: 50, Go: 50 })).toBe('Go')
    expect(mainLanguage({})).toBeNull()
  })
})
