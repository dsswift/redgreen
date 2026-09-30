import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Logger } from './log.ts'

/** Encrypts provider credentials before they are written to the database. */
export class SecretBox {
  private readonly key: Buffer

  private constructor(key: Buffer) {
    this.key = key
  }

  /** Uses the configured key, or generates one into `dataDir` so a local run works without setup. */
  static load(configured: string | undefined, dataDir: string, log: Logger): SecretBox {
    if (configured) {
      log.info('secret key loaded from environment')
      return new SecretBox(decodeKey(configured))
    }
    mkdirSync(dataDir, { recursive: true })
    const path = join(dataDir, 'secret.key')
    if (existsSync(path)) {
      log.info('secret key loaded from data dir', { path })
      return new SecretBox(decodeKey(readFileSync(path, 'utf8').trim()))
    }
    const key = randomBytes(32)
    writeFileSync(path, key.toString('base64') + '\n', { mode: 0o600 })
    log.warn('secret key generated; set SECRET_KEY to keep it outside the data dir', { path })
    return new SecretBox(key)
  }

  seal(plaintext: string): string {
    const iv = randomBytes(12)
    const cipher = createCipheriv('aes-256-gcm', this.key, iv)
    const body = Buffer.concat([cipher.update(plaintext, 'utf8'), cipher.final()])
    return Buffer.concat([iv, cipher.getAuthTag(), body]).toString('base64')
  }

  open(sealed: string): string {
    const buf = Buffer.from(sealed, 'base64')
    const decipher = createDecipheriv('aes-256-gcm', this.key, buf.subarray(0, 12))
    decipher.setAuthTag(buf.subarray(12, 28))
    return Buffer.concat([decipher.update(buf.subarray(28)), decipher.final()]).toString('utf8')
  }
}

function decodeKey(encoded: string): Buffer {
  const key = Buffer.from(encoded, 'base64')
  if (key.length !== 32) throw new Error('SECRET_KEY must be 32 bytes, base64 encoded')
  return key
}
