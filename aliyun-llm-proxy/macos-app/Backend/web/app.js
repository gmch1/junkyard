const byId = (id) => document.getElementById(id)

const state = {
  timer: null,
  toastTimer: null,
}

function formatInteger(value) {
  return new Intl.NumberFormat('zh-CN').format(Number(value || 0))
}

function formatUptime(seconds) {
  const total = Number(seconds || 0)
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (days) return `${days} 天 ${hours} 小时`
  if (hours) return `${hours} 小时 ${minutes} 分钟`
  if (minutes) return `${minutes} 分钟`
  return `${Math.max(0, Math.floor(total))} 秒`
}

function showToast(message, isError = false) {
  const toast = byId('toast')
  byId('toast-message').textContent = message
  toast.classList.toggle('error', isError)
  toast.classList.add('visible')
  window.clearTimeout(state.toastTimer)
  state.toastTimer = window.setTimeout(() => toast.classList.remove('visible'), 2400)
}

function setConnectionState(online, message = '') {
  byId('service-pill').classList.toggle('offline', !online)
  byId('service-status').textContent = online ? '代理服务运行中' : '服务连接失败'
  byId('metric-status').textContent = online ? '运行中' : '离线'
  const notice = byId('error-notice')
  notice.classList.toggle('hidden', online)
  if (message) byId('error-message').textContent = message
}

function render(data) {
  setConnectionState(true)
  byId('base-url').textContent = data.base_url || '—'
  byId('client-key').textContent = data.client_key || '—'
  byId('model-name').textContent = data.model_alias || 'aliyun-translate-auto'
  byId('metric-models').textContent = formatInteger(data.available_models)
  byId('metric-requests').textContent = formatInteger(data.client?.requests)
  byId('metric-request-detail').textContent = `成功 ${formatInteger(data.client?.successes)} · 失败 ${formatInteger(data.client?.failures)}`
  byId('metric-uptime').textContent = formatUptime(data.uptime_seconds)

  const configured = Boolean(data.configured)
  byId('configured-state').classList.toggle('ready', configured)
  byId('configured-title').textContent = configured ? 'API Key 已配置' : '等待配置'
  byId('configured-detail').textContent = configured
    ? '新密钥保存后会立即生效，无需重启服务。'
    : '填写 API Key 后代理即可处理请求。'
  byId('save-key').querySelector('span').textContent = configured ? '更新配置' : '保存配置'
}

async function refresh() {
  try {
    const response = await fetch('/admin/status', { cache: 'no-store' })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    render(await response.json())
  } catch (error) {
    setConnectionState(false, `暂时无法连接本机服务（${error.message}），页面会自动重试。`)
  } finally {
    window.clearTimeout(state.timer)
    state.timer = window.setTimeout(refresh, 3000)
  }
}

async function copyText(value) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(value)
    return
  }
  const input = document.createElement('textarea')
  input.value = value
  input.style.position = 'fixed'
  input.style.opacity = '0'
  document.body.appendChild(input)
  input.select()
  document.execCommand('copy')
  input.remove()
}

document.querySelectorAll('[data-copy]').forEach((button) => {
  button.addEventListener('click', async () => {
    const value = byId(button.dataset.copy).textContent.trim()
    if (!value || value === '—' || value === '正在获取…') return
    try {
      await copyText(value)
      showToast('已复制到剪贴板')
    } catch {
      showToast('复制失败，请手动选择文本', true)
    }
  })
})

byId('reveal-key').addEventListener('click', () => {
  const input = byId('api-key')
  input.type = input.type === 'password' ? 'text' : 'password'
})

byId('key-form').addEventListener('submit', async (event) => {
  event.preventDefault()
  const input = byId('api-key')
  const value = input.value.trim()
  if (value.length < 8) {
    showToast('API Key 长度或格式不正确', true)
    input.focus()
    return
  }
  const button = byId('save-key')
  button.disabled = true
  button.querySelector('span').textContent = '正在保存…'
  try {
    const response = await fetch('/admin/upstream-key', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Aliyun-Proxy-Admin': '1',
      },
      body: JSON.stringify({ api_key: value }),
    })
    const payload = await response.json()
    if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`)
    input.value = ''
    input.type = 'password'
    render(payload)
    showToast('配置已保存并立即生效')
  } catch (error) {
    showToast(error.message || '保存失败', true)
  } finally {
    button.disabled = false
    button.querySelector('span').textContent = byId('configured-state').classList.contains('ready') ? '更新配置' : '保存配置'
  }
})

void refresh()
