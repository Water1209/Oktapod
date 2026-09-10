// One browser document owns one authenticated workspace. Login navigates to a
// new document; an invalidated workspace is never reused for another account.
export class AuthLifecycle {
  private invalid = false
  private readonly controller = new AbortController()
  private readonly listeners = new Set<() => void>()
  readonly signal = this.controller.signal
  readonly snapshot = () => this.invalid
  readonly subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }
  invalidate = () => {
    if (this.invalid) return
    this.invalid = true
    this.controller.abort()
    for (const listener of this.listeners) listener()
  }
}

export const authLifecycle = new AuthLifecycle()
