// Login polling for the add-account wizard. Kept as a pure function so the
// failure/cancel/late-result behaviour is testable without a DOM: the wizard
// wraps it with its own session generation token.
//
// Contract:
//   - a cancelled attempt (sleep reports the attempt is gone, or the session
//     is no longer current) stops polling and reports 'cancelled' - it never
//     reports success or failure for a superseded login;
//   - a late status response that arrives after cancellation is discarded, so
//     a retry or a pasted callback cannot be overwritten by the old poll;
//   - a provider error is reported as 'error', leaving the caller free to
//     return the wizard to an editable state instead of a stuck "polling".

export type LoginStatusEnvelope = {
  login?: { status?: string; message?: string }
}

// Cancel the timer and settle its promise so a superseded poll can exit.
export function createLoginDelay() {
  let timer: ReturnType<typeof setTimeout> | undefined
  let settle: ((alive: boolean) => void) | undefined
  const cancel = () => {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
    const resolve = settle
    settle = undefined
    resolve?.(false)
  }
  return {
    cancel,
    wait(milliseconds: number): Promise<boolean> {
      cancel()
      return new Promise((resolve) => {
        settle = resolve
        timer = setTimeout(() => {
          timer = undefined
          settle = undefined
          resolve(true)
        }, milliseconds)
      })
    },
  }
}

export type LoginPollDependencies = {
  attempts: number
  /** Waits one interval; resolves false when the attempt was cancelled. */
  sleep: () => Promise<boolean>
  fetchStatus: () => Promise<LoginStatusEnvelope>
  isCancelled: () => boolean
  /** Receives each non-empty status message while the attempt is current. */
  onMessage: (message: string) => void
  timeoutMessage: string
}

export type LoginPollResult =
  | { kind: 'ok' }
  | { kind: 'error'; message: string }
  | { kind: 'cancelled' }

export async function pollLogin(deps: LoginPollDependencies): Promise<LoginPollResult> {
  const attempts = Math.max(0, Math.trunc(deps.attempts))
  for (let attempt = 0; attempt < attempts; attempt++) {
    const alive = await deps.sleep()
    if (!alive || deps.isCancelled()) return { kind: 'cancelled' }
    const status = await deps.fetchStatus()
    if (deps.isCancelled()) return { kind: 'cancelled' }
    const login = status.login || {}
    if (login.message) deps.onMessage(login.message)
    if (login.status === 'ok') return { kind: 'ok' }
    if (login.status === 'error') return { kind: 'error', message: login.message || 'login failed' }
    if (attempt === attempts - 1) return { kind: 'error', message: deps.timeoutMessage }
  }
  return { kind: 'error', message: deps.timeoutMessage }
}
