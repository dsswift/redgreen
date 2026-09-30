// Health rules. Each rule is one reason a repo can stop being green. Its
// severity is configurable globally and per repo.

export const SEVERITIES = ['red', 'amber', 'info', 'off'] as const
export type Severity = (typeof SEVERITIES)[number]

export const RULES = {
  default_branch_failing: {
    label: 'Default branch failing',
    description: 'The latest finished run of a pipeline on the default branch failed.',
    defaultSeverity: 'red',
  },
  schedule_stale: {
    label: 'Schedule stopped',
    description: 'A scheduled pipeline missed its runs, or the forge switched it off for inactivity.',
    defaultSeverity: 'amber',
  },
  release_build_failed: {
    label: 'Release build failed',
    description: 'A pipeline run for the newest release or tag failed.',
    defaultSeverity: 'red',
  },
  dependabot_critical: {
    label: 'Critical dependency alerts',
    description: 'Open critical-severity Dependabot alerts.',
    defaultSeverity: 'red',
  },
  code_scanning_critical: {
    label: 'Critical code scanning alerts',
    description: 'Open critical-severity code scanning alerts.',
    defaultSeverity: 'red',
  },
  pr_checks_failing: {
    label: 'Pull request checks failing',
    description: 'An open, ready-for-review pull request has failing checks.',
    defaultSeverity: 'red',
  },
} as const satisfies Record<string, { label: string; description: string; defaultSeverity: Severity }>

export type RuleKey = keyof typeof RULES
export const RULE_KEYS = Object.keys(RULES) as RuleKey[]

export type RuleSeverities = Record<RuleKey, Severity>
export type RuleOverrides = Partial<RuleSeverities>

export function defaultRuleSeverities(): RuleSeverities {
  return Object.fromEntries(RULE_KEYS.map((key) => [key, RULES[key].defaultSeverity])) as RuleSeverities
}

/** The board color of a repo. Quiet repos are shown apart and never color the estate. */
export type Level = 'red' | 'amber' | 'green' | 'quiet' | 'unknown'

export type QuietReason = 'archived' | 'muted' | 'no_pipelines'

export interface Signal {
  rule: RuleKey
  severity: Exclude<Severity, 'off'>
  title: string
  detail: string
  /** When the condition started, as far back as the synced history reaches. */
  since: string | null
  /** True when history ran out before the condition started, so it began earlier than `since`. */
  sinceIsLowerBound: boolean
  url: string | null
  pipelineId: string | null
}

export interface RepoHealth {
  level: Level
  quietReason: QuietReason | null
  signals: Signal[]
  running: boolean
  /** Share of finished default-branch runs that succeeded, over the synced history. */
  successRate: number | null
  lastRunAt: string | null
}

export interface RepoSettings {
  muted: boolean
  mutedPipelines: string[]
  ruleOverrides: RuleOverrides
}

export const DEFAULT_REPO_SETTINGS: RepoSettings = { muted: false, mutedPipelines: [], ruleOverrides: {} }

const LEVEL_RANK: Record<Level, number> = { red: 0, amber: 1, unknown: 2, green: 3, quiet: 4 }

export function compareLevels(a: Level, b: Level): number {
  return LEVEL_RANK[a] - LEVEL_RANK[b]
}
