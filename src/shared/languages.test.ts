import { describe, expect, it } from 'vitest'
import { LANGUAGES, languageOfFile, listedLanguage, mainLanguage } from './languages.ts'

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

describe('the language of one file', () => {
  it('comes from its extension, and a file that is not code has none', () => {
    expect(languageOfFile('src/Api/Run.cs')).toBe('C#')
    expect(languageOfFile('web/app/page.TSX')).toBe('TypeScript')
    expect(languageOfFile('infra/main.tf')).toBe('Terraform')
    expect(languageOfFile('App.java')).toBe('Other')
    expect(languageOfFile('README.md')).toBeNull()
    expect(languageOfFile('Dockerfile')).toBeNull()
    expect(languageOfFile('.gitignore')).toBeNull()
  })
})
