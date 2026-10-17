export const busyStates = new Set(['preparing', 'preparing_image', 'checking', 'backing_up', 'submitting', 'running', 'queued', 'pulling', 'host_binary', 'image_ready', 'recreating', 'rolling_back', 'downloading', 'verifying', 'extracting', 'stopping', 'replacing', 'starting'])
export const applyJobStates = new Set(['backing_up', 'running'])
export const applyAgentStates = new Set(['recreating', 'rolling_back', 'stopping', 'replacing', 'starting'])
export const progressAgentStates = new Set(['queued', 'pulling', 'host_binary', 'image_ready', 'preparing', 'recreating', 'checking', 'rolling_back', 'downloading', 'verifying', 'extracting', 'stopping', 'replacing', 'starting'])
export function updateFailed(state?: string) {
  return ['failed', 'rolled_back', 'recovery_failed', 'download_failed', 'verification_failed'].includes(state || '')
}
