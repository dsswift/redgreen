import { describe, expect, it } from 'vitest'
import { parseSchedules } from './workflow-file.ts'

describe('parseSchedules', () => {
  it('reads every cron under on.schedule', () => {
    const yaml = `
name: sync
on:
  push:
    branches: [main]
  schedule:
    - cron: '0 6 * * *'
    - cron: "30 18 * * 1-5"
jobs: {}
`
    expect(parseSchedules(yaml)).toEqual(['0 6 * * *', '30 18 * * 1-5'])
  })

  it('returns nothing for workflows without a schedule or with broken yaml', () => {
    expect(parseSchedules('on: [push, pull_request]\njobs: {}')).toEqual([])
    expect(parseSchedules('on: push')).toEqual([])
    expect(parseSchedules('on: {schedule: [{cron: 1}]}')).toEqual([])
    expect(parseSchedules(':: not yaml [')).toEqual([])
  })
})
