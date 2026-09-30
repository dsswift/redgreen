import { EventEmitter } from 'node:events'
import type { ServerEvent } from '../shared/api.ts'

/** In-process fan-out of changes to every open browser stream. */
export class Events {
  private readonly emitter = new EventEmitter()

  constructor() {
    this.emitter.setMaxListeners(0)
  }

  emit(event: ServerEvent): void {
    this.emitter.emit('event', event)
  }

  subscribe(listener: (event: ServerEvent) => void): () => void {
    this.emitter.on('event', listener)
    return () => this.emitter.off('event', listener)
  }
}
