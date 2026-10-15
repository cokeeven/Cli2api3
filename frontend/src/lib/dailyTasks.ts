export function dailyStepLabel(step: string, t: (key: string) => string) {
  if (step.startsWith('growth:')) {
    const code = step.slice('growth:'.length)
    const key = `dailyGrowth_${code.replaceAll('.', '_')}`
    const translated = t(key)
    return `${t('dailyStep_growth')} · ${translated === key ? code : translated}`
  }
  const key = `dailyStep_${step}`
  const translated = t(key)
  return translated === key ? step : translated
}

export function dailyStatusColor(status: string) {
  if (status === 'success' || status === 'already') return 'success' as const
  if (status === 'partial' || status === 'running') return 'warning' as const
  if (status === 'error' || status === 'interrupted') return 'danger' as const
  return 'default' as const
}
