// The languages a repository's main language is picked from. Every forge
// names languages its own way; one list makes them say it the same way.

/** In the order the hub offers them. A language that is not here counts as `Other`. */
export const LANGUAGES = ['C#', 'Go', 'Python', 'TypeScript', 'JavaScript', 'Terraform', 'Bicep', 'PowerShell', 'Shell', 'Swift', 'Rust', 'C++', 'Lua', 'GDScript', 'HTML', 'CSS', 'SQL', 'Other'] as const
export type Language = (typeof LANGUAGES)[number]

/** What a forge calls a language, where that differs from the name on the list. */
const NAMED: Readonly<Record<string, Language>> = {
  HCL: 'Terraform',
  TSX: 'TypeScript',
  JSX: 'JavaScript',
  Vue: 'JavaScript',
  Svelte: 'JavaScript',
  SCSS: 'CSS',
  Sass: 'CSS',
  Less: 'CSS',
  Batchfile: 'Shell',
  TSQL: 'SQL',
  PLpgSQL: 'SQL',
  PLSQL: 'SQL',
  'Jupyter Notebook': 'Python',
  C: 'C++',
  'Objective-C': 'Swift',
}

/** The name on the list for what a forge reported, `Other` when it is not on it, and null for no language. */
export function listedLanguage(name: string | null | undefined): Language | null {
  if (!name) return null
  const named = NAMED[name] ?? name
  return (LANGUAGES as readonly string[]).includes(named) ? (named as Language) : 'Other'
}

/**
 * The listed language with the largest share of a repository. Shares a forge
 * reports under different names for one language are added together. A
 * language on the list beats `Other` however large `Other` is, since `Other`
 * is mostly build and config files. A tie goes to the first by name, so the
 * answer does not flap. Null when there is no code.
 */
export function mainLanguage(shares: Record<string, number>): Language | null {
  const sum = new Map<Language, number>()
  for (const [name, share] of Object.entries(shares)) {
    const listed = listedLanguage(name)
    if (listed) sum.set(listed, (sum.get(listed) ?? 0) + share)
  }
  const ranked = [...sum].filter(([name]) => name !== 'Other').sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  return ranked[0]?.[0] ?? (sum.size ? 'Other' : null)
}
